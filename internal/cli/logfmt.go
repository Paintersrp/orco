package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/example/orco/internal/engine"
	"github.com/example/orco/internal/runtime"
)

type logRecord struct {
	Timestamp time.Time `json:"ts"`
	Service   string    `json:"service"`
	Replica   int       `json:"replica"`
	Level     string    `json:"level"`
	Message   string    `json:"msg"`
	Source    string    `json:"source"`
}

func newLogRecord(event engine.Event) logRecord {
	level := event.Level
	if level == "" {
		level = "info"
	}
	source := event.Source
	if source == "" {
		source = runtime.LogSourceSystem
	}
	return logRecord{
		Timestamp: event.Timestamp,
		Service:   event.Service,
		Replica:   event.Replica,
		Level:     level,
		Message:   event.Message,
		Source:    source,
	}
}

func encodeLogEvent(enc *json.Encoder, stderr io.Writer, event engine.Event) {
	if enc == nil {
		return
	}
	record := newLogRecord(event)
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now()
	}
	if err := enc.Encode(&record); err != nil {
		fmt.Fprintf(stderr, "error: encode log: %v\n", err)
	}
}
