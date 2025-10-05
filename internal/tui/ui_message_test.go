package tui

import (
	"errors"
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/engine"
)

func TestFormatEventMessage(t *testing.T) {
	tests := []struct {
		name string
		evt  engine.Event
		want string
	}{
		{
			name: "message only",
			evt:  engine.Event{Message: "starting up"},
			want: "starting up",
		},
		{
			name: "error only",
			evt:  engine.Event{Err: errors.New("failed to connect")},
			want: "failed to connect",
		},
		{
			name: "message and error",
			evt:  engine.Event{Message: "start failed", Err: errors.New("exit status 1")},
			want: "start failed: exit status 1",
		},
		{
			name: "message and reason",
			evt:  engine.Event{Message: "probe failed", Reason: engine.ReasonProbeUnready},
			want: "probe failed (probe_unready)",
		},
		{
			name: "error and reason",
			evt:  engine.Event{Err: errors.New("connection refused"), Reason: engine.ReasonRestart},
			want: "connection refused (restart)",
		},
		{
			name: "reason only",
			evt:  engine.Event{Reason: engine.ReasonRestart},
			want: "restart",
		},
		{
			name: "message, error, and reason",
			evt:  engine.Event{Message: "crashed", Err: errors.New("signal: 9"), Reason: engine.ReasonRestart},
			want: "crashed: signal: 9 (restart)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatEventMessage(tt.evt); got != tt.want {
				t.Fatalf("formatEventMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatEventMessageRedactsSecrets(t *testing.T) {
	evt := engine.Event{
		Message: "loading ${TOKEN}",
		Err:     errors.New("API_KEY=abcd"),
	}

	want := "loading ${[redacted]}: API_KEY=[redacted]"
	if got := formatEventMessage(evt); got != want {
		t.Fatalf("formatEventMessage() = %q, want %q", got, want)
	}
}

func TestApplyEventLockedRedactsServiceEvents(t *testing.T) {
	ui := newTestUI(t)

	evt := engine.Event{
		Timestamp: time.Now(),
		Service:   "api",
		Replica:   -1,
		Type:      engine.EventTypeFailed,
		Message:   "loading ${TOKEN}",
		Err:       errors.New("API_KEY=abcd"),
	}

	ui.applyEventLocked(evt)

	state := ui.services["api"]
	if state == nil {
		t.Fatalf("expected service state to be created")
	}

	wantMessage := "loading ${[redacted]}: API_KEY=[redacted]"
	if state.message != wantMessage {
		t.Fatalf("state message = %q, want %q", state.message, wantMessage)
	}

	if len(state.events) != 1 {
		t.Fatalf("expected one service event, got %d", len(state.events))
	}

	record := state.events[0]
	if record.Message != "loading ${[redacted]}" {
		t.Fatalf("record.Message = %q, want %q", record.Message, "loading ${[redacted]}")
	}
	if record.Error != "API_KEY=[redacted]" {
		t.Fatalf("record.Error = %q, want %q", record.Error, "API_KEY=[redacted]")
	}
}
