package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/engine"
)

func TestUIApplyEventAggregatesReplicas(t *testing.T) {
	ui := newTestUI(t)

	base := time.Now()
	ui.applyEventLocked(engine.Event{Service: "api", Replica: 0, Type: engine.EventTypeStarting, Timestamp: base})
	ui.applyEventLocked(engine.Event{Service: "api", Replica: 1, Type: engine.EventTypeStarting, Timestamp: base.Add(5 * time.Millisecond)})

	state := ui.services["api"]
	if state == nil {
		t.Fatalf("expected service state to be created")
	}
	if state.replicaCount != 2 {
		t.Fatalf("expected replica count 2, got %d", state.replicaCount)
	}
	if state.ready {
		t.Fatalf("expected service to be unready until replicas ready")
	}

	ui.applyEventLocked(engine.Event{Service: "api", Replica: 0, Type: engine.EventTypeReady, Message: "ready", Timestamp: base.Add(10 * time.Millisecond)})

	state = ui.services["api"]
	if state.ready {
		t.Fatalf("expected service to remain unready until all replicas ready")
	}

	ui.applyEventLocked(engine.Event{Service: "api", Replica: 1, Type: engine.EventTypeReady, Message: "ready", Timestamp: base.Add(15 * time.Millisecond)})

	state = ui.services["api"]
	if !state.ready {
		t.Fatalf("expected service to become ready once all replicas ready")
	}

	ui.applyEventLocked(engine.Event{Service: "api", Replica: 1, Type: engine.EventTypeCrashed, Message: "boom", Timestamp: base.Add(20 * time.Millisecond)})

	state = ui.services["api"]
	if state.ready {
		t.Fatalf("expected service to be unready after replica crash")
	}
	if state.restarts != 1 {
		t.Fatalf("expected restarts=1, got %d", state.restarts)
	}
	if state.state != engine.EventTypeCrashed {
		t.Fatalf("expected crashed state, got %q", state.state)
	}
	if state.message == "" || !strings.Contains(state.message, "replica 1") {
		t.Fatalf("expected message to reference replica, got %q", state.message)
	}
}
