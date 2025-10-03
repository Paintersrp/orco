package cliutil

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/example/orco/internal/engine"
	"github.com/example/orco/internal/runtime"
)

// LogRecord represents a structured log event ready for JSON encoding.
type LogRecord struct {
	Timestamp time.Time `json:"ts"`
	Service   string    `json:"service"`
	Replica   int       `json:"replica"`
	Level     string    `json:"level"`
	Message   string    `json:"msg"`
	Source    string    `json:"source"`
}

// NewLogRecord converts an engine event into a structured log record.
func NewLogRecord(event engine.Event) LogRecord {
	level := event.Level
	if level == "" {
		level = "info"
	}
	source := event.Source
	if source == "" {
		source = runtime.LogSourceSystem
	}
	return LogRecord{
		Timestamp: event.Timestamp,
		Service:   event.Service,
		Replica:   event.Replica,
		Level:     level,
		Message:   event.Message,
		Source:    source,
	}
}

// EncodeLogEvent encodes a log event to JSON, reporting errors to stderr if needed.
func EncodeLogEvent(enc *json.Encoder, stderr io.Writer, event engine.Event) {
	if enc == nil {
		return
	}
	record := NewLogRecord(event)
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now()
	}
	if err := enc.Encode(&record); err != nil {
		fmt.Fprintf(stderr, "error: encode log: %v\n", err)
	}
}
