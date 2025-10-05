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

	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (1 log + drop metadata), got %d", len(events))
	}

	var logSeen bool
	var metaEvent *engine.Event
	for i := range events {
		evt := events[i]
		if evt.Type == engine.EventTypeLog && evt.Message == "line-1" && !logSeen {
			logSeen = true
		}
		if evt.Message == "dropped=2" && evt.Source == runtime.LogSourceSystem {
			metaEvent = &events[i]
		}
	}
	if !logSeen {
		t.Fatalf("expected to observe the original log entry, events=%v", events)
	}
	if metaEvent == nil {
		t.Fatalf("expected drop metadata event, got %v", events)
	}
	if metaEvent.Service != "api" {
		t.Fatalf("meta event service mismatch: got %s", metaEvent.Service)
	}
	if metaEvent.Level != "warn" {
		t.Fatalf("expected meta level warn, got %s", metaEvent.Level)
	}
	if time.Since(metaEvent.Timestamp) > time.Second {
		t.Fatalf("expected recent timestamp, got %v", metaEvent.Timestamp)
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
