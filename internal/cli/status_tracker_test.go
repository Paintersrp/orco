package cli

import (
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/engine"
)

func TestStatusTrackerUpdatesReadyAndBlockedState(t *testing.T) {
	t.Parallel()

	tracker := newStatusTracker()
	base := time.Now().Add(-10 * time.Second)

	tracker.Apply(engine.Event{Service: "api", Type: engine.EventTypeStarting, Timestamp: base})
	tracker.Apply(engine.Event{Service: "api", Type: engine.EventTypeBlocked, Message: "waiting", Timestamp: base.Add(time.Second)})

	snapshot := tracker.Snapshot()["api"]
	if snapshot.State != engine.EventTypeBlocked {
		t.Fatalf("expected blocked state, got %q", snapshot.State)
	}
	if snapshot.Ready {
		t.Fatalf("expected ready=false after blocked event")
	}
	if snapshot.Message != "waiting" {
		t.Fatalf("expected message to be retained, got %q", snapshot.Message)
	}

	tracker.Apply(engine.Event{Service: "api", Type: engine.EventTypeReady, Message: "ready", Timestamp: base.Add(2 * time.Second)})

	snapshot = tracker.Snapshot()["api"]
	if snapshot.State != engine.EventTypeReady {
		t.Fatalf("expected ready state, got %q", snapshot.State)
	}
	if !snapshot.Ready {
		t.Fatalf("expected ready=true after ready event")
	}
	if snapshot.Message != "ready" {
		t.Fatalf("expected message to update, got %q", snapshot.Message)
	}
}
