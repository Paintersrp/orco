package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/engine"
)

func TestStatusTrackerUpdatesReadyAndBlockedState(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker()
	base := time.Now().Add(-10 * time.Second)

	tracker.Apply(engine.Event{Service: "api", Replica: -1, Type: engine.EventTypeStarting, Timestamp: base})
	tracker.Apply(engine.Event{Service: "api", Replica: -1, Type: engine.EventTypeBlocked, Message: "waiting", Timestamp: base.Add(time.Second)})

	snapshot := tracker.Snapshot()["api"]
	if snapshot.State != engine.EventTypeBlocked {
		t.Fatalf("expected blocked state, got %q", snapshot.State)
	}
	if snapshot.Ready {
		t.Fatalf("expected ready=false after blocked event")
	}
	if snapshot.ReadyReplicas != 0 {
		t.Fatalf("expected ready replicas to remain 0, got %d", snapshot.ReadyReplicas)
	}
	if snapshot.Message != "waiting" {
		t.Fatalf("expected message to be retained, got %q", snapshot.Message)
	}

	tracker.Apply(engine.Event{Service: "api", Replica: 0, Type: engine.EventTypeReady, Message: "ready", Timestamp: base.Add(2 * time.Second)})

	snapshot = tracker.Snapshot()["api"]
	if snapshot.State != engine.EventTypeReady {
		t.Fatalf("expected ready state, got %q", snapshot.State)
	}
	if !snapshot.Ready {
		t.Fatalf("expected ready=true after ready event")
	}
	if snapshot.ReadyReplicas != 1 {
		t.Fatalf("expected readyReplicas=1 after ready event, got %d", snapshot.ReadyReplicas)
	}
	if snapshot.Message != "replica 0: ready" {
		t.Fatalf("expected message to update, got %q", snapshot.Message)
	}
}

func TestStatusTrackerAggregatesReplicaReadiness(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker()
	base := time.Now()

	tracker.Apply(engine.Event{Service: "api", Replica: 0, Type: engine.EventTypeStarting, Timestamp: base})
	tracker.Apply(engine.Event{Service: "api", Replica: 1, Type: engine.EventTypeStarting, Timestamp: base.Add(10 * time.Millisecond)})

	snap := tracker.Snapshot()["api"]
	if snap.Ready {
		t.Fatalf("expected service to remain unready until replicas ready")
	}
	if snap.Replicas != 2 {
		t.Fatalf("expected replica count 2, got %d", snap.Replicas)
	}
	if snap.ReadyReplicas != 0 {
		t.Fatalf("expected ready replicas 0, got %d", snap.ReadyReplicas)
	}

	tracker.Apply(engine.Event{Service: "api", Replica: 0, Type: engine.EventTypeReady, Message: "ready", Timestamp: base.Add(20 * time.Millisecond)})

	snap = tracker.Snapshot()["api"]
	if snap.Ready {
		t.Fatalf("expected service to remain unready until all replicas ready")
	}
	if snap.ReadyReplicas != 1 {
		t.Fatalf("expected ready replicas 1, got %d", snap.ReadyReplicas)
	}

	tracker.Apply(engine.Event{Service: "api", Replica: 1, Type: engine.EventTypeReady, Message: "ready", Timestamp: base.Add(30 * time.Millisecond)})

	snap = tracker.Snapshot()["api"]
	if !snap.Ready {
		t.Fatalf("expected service to report ready once all replicas ready")
	}
	if snap.ReadyReplicas != 2 {
		t.Fatalf("expected ready replicas 2, got %d", snap.ReadyReplicas)
	}

	tracker.Apply(engine.Event{Service: "api", Replica: 1, Type: engine.EventTypeCrashed, Message: "boom", Timestamp: base.Add(40 * time.Millisecond)})

	snap = tracker.Snapshot()["api"]
	if snap.Ready {
		t.Fatalf("expected service to be unready after replica crash")
	}
	if snap.Restarts != 1 {
		t.Fatalf("expected restarts=1 after crash, got %d", snap.Restarts)
	}
	if snap.ReadyReplicas != 1 {
		t.Fatalf("expected ready replicas to drop to 1, got %d", snap.ReadyReplicas)
	}
	if snap.State != engine.EventTypeCrashed {
		t.Fatalf("expected crashed state, got %q", snap.State)
	}
	if !strings.Contains(snap.Message, "replica 1") {
		t.Fatalf("expected message to reference replica, got %q", snap.Message)
	}
}

func TestStatusTrackerRecordsOOMError(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker()
	base := time.Now()

	oomErr := errors.New("container terminated by the kernel OOM killer (memory limit 256Mi): container exited with status 137")
	tracker.Apply(engine.Event{
		Service:   "api",
		Replica:   0,
		Type:      engine.EventTypeCrashed,
		Message:   "instance crashed",
		Err:       oomErr,
		Timestamp: base,
	})

	snap := tracker.Snapshot()["api"]
	if snap.State != engine.EventTypeCrashed {
		t.Fatalf("expected crashed state, got %q", snap.State)
	}
	if !strings.Contains(snap.Message, "instance crashed") {
		t.Fatalf("expected event message to be included, got %q", snap.Message)
	}
	if !strings.Contains(snap.Message, "OOM killer") {
		t.Fatalf("expected OOM reason in message, got %q", snap.Message)
	}
	if !strings.Contains(snap.Message, "replica 0") {
		t.Fatalf("expected replica identifier in message, got %q", snap.Message)
	}
}

func TestStatusTrackerRedactsSecretsInMessages(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker()
	base := time.Now()

	tracker.Apply(engine.Event{
		Service:   "api",
		Replica:   0,
		Type:      engine.EventTypeFailed,
		Message:   "unable to fetch ${API_TOKEN} AWS_SECRET_ACCESS_KEY=shhh",
		Timestamp: base,
	})

	snap := tracker.Snapshot()["api"]
	if strings.Contains(snap.Message, "${API_TOKEN}") {
		t.Fatalf("expected template placeholder to be redacted, got %q", snap.Message)
	}
	if !strings.Contains(snap.Message, "${[redacted]}") {
		t.Fatalf("expected template placeholder marker, got %q", snap.Message)
	}
	if strings.Contains(snap.Message, "shhh") {
		t.Fatalf("expected secret value to be redacted, got %q", snap.Message)
	}
	if !strings.Contains(snap.Message, "AWS_SECRET_ACCESS_KEY=[redacted]") {
		t.Fatalf("expected known secret key redacted, got %q", snap.Message)
	}
}

func TestStatusTrackerTracksPromotionStates(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker()
	base := time.Now()

	tracker.Apply(engine.Event{Service: "api", Replica: 0, Type: engine.EventTypeCanary, Message: "canary replica ready", Timestamp: base})

	snap := tracker.Snapshot()["api"]
	if snap.State != engine.EventTypeCanary {
		t.Fatalf("expected canary state, got %q", snap.State)
	}
	if !strings.Contains(snap.Message, "canary replica ready") {
		t.Fatalf("expected canary message to be recorded, got %q", snap.Message)
	}

	tracker.Apply(engine.Event{Service: "api", Replica: -1, Type: engine.EventTypePromoted, Message: "promotion complete", Timestamp: base.Add(time.Second)})

	snap = tracker.Snapshot()["api"]
	if snap.State != engine.EventTypePromoted {
		t.Fatalf("expected promoted state, got %q", snap.State)
	}
	if !strings.Contains(snap.Message, "promotion complete") {
		t.Fatalf("expected promoted message, got %q", snap.Message)
	}

	tracker.Apply(engine.Event{Service: "api", Replica: 0, Type: engine.EventTypeAborted, Err: errors.New("canary failed"), Timestamp: base.Add(2 * time.Second)})

	snap = tracker.Snapshot()["api"]
	if snap.State != engine.EventTypeAborted {
		t.Fatalf("expected aborted state, got %q", snap.State)
	}
	if !strings.Contains(snap.Message, "canary failed") {
		t.Fatalf("expected aborted message to reference error, got %q", snap.Message)
	}
}

func TestStatusTrackerSetResourceHints(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker()
	base := time.Now()

	tracker.Apply(engine.Event{Service: "api", Replica: -1, Type: engine.EventTypeStarting, Timestamp: base})

	tracker.SetResourceHints(map[string]ResourceHint{
		"api": {
			CPU:    "500m",
			Memory: "256MiB",
		},
	})

	snap := tracker.Snapshot()["api"]
	if snap.Resources.CPU != "500m" {
		t.Fatalf("expected cpu hint to be recorded, got %q", snap.Resources.CPU)
	}
	if snap.Resources.Memory != "256MiB" {
		t.Fatalf("expected memory hint to be recorded, got %q", snap.Resources.Memory)
	}

	tracker.SetResourceHints(map[string]ResourceHint{})
	snap = tracker.Snapshot()["api"]
	if snap.Resources.CPU != "" || snap.Resources.Memory != "" {
		t.Fatalf("expected hints to reset when absent, got %+v", snap.Resources)
	}
}

func TestStatusTrackerHistoryRingBuffer(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker(WithHistorySize(3))
	base := time.Now().Add(-5 * time.Minute)
	types := []engine.EventType{
		engine.EventTypeStarting,
		engine.EventTypeReady,
		engine.EventTypeUnready,
		engine.EventTypeStopping,
		engine.EventTypeFailed,
	}
	reasons := []string{
		engine.ReasonInitialStart,
		engine.ReasonProbeReady,
		engine.ReasonProbeUnready,
		engine.ReasonSupervisorStop,
		engine.ReasonStartFailure,
	}
	for i := 0; i < len(types); i++ {
		tracker.Apply(engine.Event{
			Service:   "api",
			Replica:   -1,
			Type:      types[i],
			Message:   fmt.Sprintf("phase-%d", i),
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Reason:    reasons[i],
		})
	}

	history := tracker.History("api", 4)
	if len(history) != 3 {
		t.Fatalf("expected 3 history entries due to ring buffer, got %d", len(history))
	}
	for idx, want := range []int{2, 3, 4} {
		if history[idx].Type != types[want] {
			t.Fatalf("unexpected event type at %d: got %s want %s", idx, history[idx].Type, types[want])
		}
		if history[idx].Reason != reasons[want] {
			t.Fatalf("unexpected reason at %d: got %s want %s", idx, history[idx].Reason, reasons[want])
		}
		if !strings.Contains(history[idx].Message, fmt.Sprintf("phase-%d", want)) {
			t.Fatalf("expected message for phase %d, got %q", want, history[idx].Message)
		}
	}

	trimmed := tracker.History("api", 2)
	if len(trimmed) != 2 {
		t.Fatalf("expected trimmed history length 2, got %d", len(trimmed))
	}
	if trimmed[0].Type != types[3] || trimmed[1].Type != types[4] {
		t.Fatalf("unexpected trimmed order: %+v", trimmed)
	}
}

func TestStatusTrackerJournalOptIn(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	journalPath := filepath.Join(dir, "journal.jsonl")

	tracker := newStatusTracker(WithHistorySize(2), WithJournalEnabled(true), WithJournalPath(journalPath))
	base := time.Now().Add(-time.Minute)
	tracker.Apply(engine.Event{Service: "api", Type: engine.EventTypeStarting, Message: "boot", Timestamp: base, Reason: engine.ReasonInitialStart})
	tracker.Apply(engine.Event{Service: "api", Type: engine.EventTypeReady, Message: "ready", Timestamp: base.Add(5 * time.Second), Reason: engine.ReasonProbeReady})

	data, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatalf("expected journal to be written: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two journal lines, got %d", len(lines))
	}
	type entry struct {
		Timestamp time.Time        `json:"timestamp"`
		Service   string           `json:"service"`
		Replica   int              `json:"replica"`
		Type      engine.EventType `json:"type"`
		Reason    string           `json:"reason"`
		Message   string           `json:"message"`
	}
	history := tracker.History("api", 2)
	for i, line := range lines {
		var e entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("failed to parse journal line %d: %v", i, err)
		}
		if e.Service != "api" {
			t.Fatalf("unexpected service in journal: %s", e.Service)
		}
		if e.Message == "" {
			t.Fatalf("expected message in journal entry %d", i)
		}
		if e.Type != history[i].Type {
			t.Fatalf("expected journal types to match history: %s vs %s", e.Type, history[i].Type)
		}
	}

	trackerNoJournal := newStatusTracker(WithJournalPath(filepath.Join(dir, "disabled.jsonl")))
	trackerNoJournal.Apply(engine.Event{Service: "api", Type: engine.EventTypeFailed, Message: "fail", Timestamp: base})
	if _, err := os.Stat(filepath.Join(dir, "disabled.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("expected journal file to remain absent when not enabled, err=%v", err)
	}
}
