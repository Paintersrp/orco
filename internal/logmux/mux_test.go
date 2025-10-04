package logmux

import (
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/engine"
	"github.com/Paintersrp/orco/internal/runtime"
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

	var events []engine.Event
	for evt := range mux.Output() {
		events = append(events, evt)
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	var apiMessages []string
	var workerMessages []string
	for _, evt := range events {
		switch evt.Service {
		case "api":
			apiMessages = append(apiMessages, evt.Message)
		case "worker":
			workerMessages = append(workerMessages, evt.Message)
		default:
			t.Fatalf("unexpected service %s", evt.Service)
		}
	}

	if len(apiMessages) != 2 {
		t.Fatalf("expected 2 api events, got %d", len(apiMessages))
	}
	if apiMessages[0] != "api ready" || apiMessages[1] != "api ok" {
		t.Fatalf("api messages out of order: %v", apiMessages)
	}
	if len(workerMessages) != 1 || workerMessages[0] != "worker ready" {
		t.Fatalf("unexpected worker messages: %v", workerMessages)
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

func TestMuxNormalizesLevelsFromMessages(t *testing.T) {
	mux := New(4)
	src := make(chan engine.Event, 3)

	mux.Add(src)

	go func() {
		src <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "ERROR: failure"}
		src <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "INFO: all good", Level: "warn", Source: runtime.LogSourceStderr}
		src <- engine.Event{Service: "api", Type: engine.EventTypeLog, Message: "plain"}
		close(src)
	}()

	go mux.Close()

	var events []engine.Event
	for evt := range mux.Output() {
		events = append(events, evt)
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	if events[0].Level != "error" {
		t.Fatalf("expected first event level error, got %s", events[0].Level)
	}
	if events[1].Level != "info" {
		t.Fatalf("expected second event level info, got %s", events[1].Level)
	}
	if events[2].Level != "info" {
		t.Fatalf("expected third event level info, got %s", events[2].Level)
	}
	for i, evt := range events {
		if evt.Source == "" {
			t.Fatalf("event %d expected normalized source, got empty", i)
		}
	}
}
