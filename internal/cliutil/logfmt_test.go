package cliutil

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/engine"
)

func TestEncodeLogEventInfersLevel(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		expected string
	}{
		{name: "errorToken", message: "[ERROR] failed to start", expected: "error"},
		{name: "warnToken", message: "WARN service requires attention", expected: "warn"},
		{name: "infoToken", message: "info: service ready", expected: "info"},
		{name: "noTokenDefaults", message: "service started", expected: "info"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			var errBuf bytes.Buffer

			event := engine.Event{
				Timestamp: time.Unix(0, 0),
				Message:   tc.message,
			}

			EncodeLogEvent(json.NewEncoder(&out), &errBuf, event)

			if errBuf.Len() != 0 {
				t.Fatalf("unexpected stderr output: %s", errBuf.String())
			}

			var record LogRecord
			if err := json.Unmarshal(out.Bytes(), &record); err != nil {
				t.Fatalf("failed to unmarshal log record: %v", err)
			}

			if record.Level != tc.expected {
				t.Fatalf("expected level %q, got %q", tc.expected, record.Level)
			}
		})
	}
}

func TestEncodeLogEventKeepsProvidedLevel(t *testing.T) {
	var out bytes.Buffer
	var errBuf bytes.Buffer

	event := engine.Event{
		Timestamp: time.Unix(0, 0),
		Message:   "custom level",
		Level:     "debug",
	}

	EncodeLogEvent(json.NewEncoder(&out), &errBuf, event)

	if errBuf.Len() != 0 {
		t.Fatalf("unexpected stderr output: %s", errBuf.String())
	}

	var record LogRecord
	if err := json.Unmarshal(out.Bytes(), &record); err != nil {
		t.Fatalf("failed to unmarshal log record: %v", err)
	}

	if record.Level != "debug" {
		t.Fatalf("expected level %q, got %q", "debug", record.Level)
	}
}

func TestNewLogRecordRedactsSecrets(t *testing.T) {
	event := engine.Event{
		Timestamp: time.Unix(0, 0),
		Message:   `sending ${API_TOKEN} AWS_SECRET_ACCESS_KEY="super-secret"`,
	}

	record := NewLogRecord(event)

	if strings.Contains(record.Message, "${API_TOKEN}") {
		t.Fatalf("expected template placeholder to be redacted, got %q", record.Message)
	}
	if !strings.Contains(record.Message, "${[redacted]}") {
		t.Fatalf("expected template placeholder marker, got %q", record.Message)
	}
	if strings.Contains(record.Message, "super-secret") {
		t.Fatalf("expected secret value to be redacted, got %q", record.Message)
	}
	if !strings.Contains(record.Message, `AWS_SECRET_ACCESS_KEY="[redacted]"`) {
		t.Fatalf("expected known secret key redacted, got %q", record.Message)
	}
}
