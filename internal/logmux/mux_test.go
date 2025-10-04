package logmux

import (
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/engine"
)

func TestMuxFansInMultipleSources(t *testing.T) {
	mux := New(4)
	src1 := make(chan engine.Event)
	src2 := make(chan engine.Event)

	mux.Add(src1)
	mux.Add(src2)

	go func() {
		src1 <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "api ready"}
		src1 <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "api ok"}
		close(src1)
	}()

	go func() {
		src2 <- engine.Event{Service: "worker", Type: engine.EventTypeLog, Message: "worker ready"}
		close(src2)
	}()

	go mux.Close()

	var services []string
	var messages []string
	for evt := range mux.Output() {
		services = append(services, evt.Service)
		messages = append(messages, evt.Message)
	}

	if len(messages) != 3 {
		t.Fatalf("expected 3 events, got %d", len(messages))
	}

	expectedServices := []string{"api", "api", "worker"}
	expectedMessages := []string{"api ready", "api ok", "worker ready"}
	for i := range expectedServices {
		if services[i] != expectedServices[i] {
			t.Fatalf("event %d service mismatch: got %s want %s", i, services[i], expectedServices[i])
		}
		if messages[i] != expectedMessages[i] {
			t.Fatalf("event %d message mismatch: got %s want %s", i, messages[i], expectedMessages[i])
		}
	}
}

func TestMuxEmitsDropMetaEvents(t *testing.T) {
	mux := New(1)
	src := make(chan engine.Event)

	mux.Add(src)

	done := make(chan struct{})
	go func() {
		src <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "line-1", Level: "info"}
		src <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "line-2", Level: "info"}
		src <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "line-3", Level: "info"}
		close(src)
		close(done)
	}()

	<-done

	go mux.Close()

	var events []engine.Event
	for evt := range mux.Output() {
		events = append(events, evt)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events (1 log + 1 meta), got %d", len(events))
	}

	if events[0].Message != "line-1" {
		t.Fatalf("expected first event to be the original log, got %q", events[0].Message)
	}

	meta := events[1]
	if meta.Service != "api" {
		t.Fatalf("meta event service mismatch: got %s", meta.Service)
	}
	if meta.Message != "dropped=2" {
		t.Fatalf("expected drop metadata, got %q", meta.Message)
	}
	if meta.Source != "orco" {
		t.Fatalf("expected meta source to be orco, got %s", meta.Source)
	}
	if meta.Level != "warn" {
		t.Fatalf("expected meta level warn, got %s", meta.Level)
	}
	if time.Since(meta.Timestamp) > time.Second {
		t.Fatalf("expected recent timestamp, got %v", meta.Timestamp)
	}
}
