package cliutil

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/Paintersrp/orco/internal/engine"
	"github.com/Paintersrp/orco/internal/runtime"
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
		if inferred := inferLogLevel(event.Message); inferred != "" {
			level = inferred
		} else {
			level = "info"
		}
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

var levelTokenPattern = regexp.MustCompile(`(?i)\b(error|warn|info)\b`)

func inferLogLevel(message string) string {
	matches := levelTokenPattern.FindStringSubmatch(message)
	if len(matches) < 2 {
		return ""
	}
	switch strings.ToLower(matches[1]) {
	case "error":
		return "error"
	case "warn":
		return "warn"
	case "info":
		return "info"
	default:
		return ""
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
