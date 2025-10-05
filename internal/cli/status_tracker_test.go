package cli

import (
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
