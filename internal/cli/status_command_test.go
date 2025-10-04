package cli

import (
	"bytes"
	stdcontext "context"
	"strings"
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/engine"
)

func TestStatusCommandReflectsBlockedAndReadyTransitions(t *testing.T) {
	t.Parallel()

	stackPath := writeStackFile(t, `version: "0.1"
stack:
  name: "demo"
  workdir: "."
defaults:
  health:
    cmd:
      command: ["true"]
services:
  api:
    runtime: process
    command: ["sleep", "0"]
  db:
    runtime: process
    command: ["sleep", "0"]
`)

	ctx := &context{stackFile: &stackPath}
	tracker := ctx.statusTracker()

	base := time.Now().Add(-2 * time.Minute)
	tracker.Apply(engine.Event{Service: "db", Type: engine.EventTypeReady, Message: "database ready", Timestamp: base})
	tracker.Apply(engine.Event{Service: "api", Type: engine.EventTypeBlocked, Message: "waiting for db", Timestamp: base.Add(30 * time.Second)})

	output := runStatusCommand(t, ctx)
	if !strings.Contains(output, "SERVICE") || !strings.Contains(output, "STATE") {
		t.Fatalf("expected status header, got: %s", output)
	}

	apiLine := findServiceLine(output, "api")
	if !strings.Contains(apiLine, "Blocked") {
		t.Fatalf("expected api line to show blocked, got: %s", apiLine)
	}
	if !strings.Contains(apiLine, "No") {
		t.Fatalf("expected api line to show ready=No, got: %s", apiLine)
	}
	if !strings.Contains(apiLine, "waiting for db") {
		t.Fatalf("expected api message in output, got: %s", apiLine)
	}

	tracker.Apply(engine.Event{Service: "api", Type: engine.EventTypeReady, Message: "service ready", Timestamp: base.Add(90 * time.Second)})

	output = runStatusCommand(t, ctx)
	apiLine = findServiceLine(output, "api")
	if !strings.Contains(apiLine, "Ready") {
		t.Fatalf("expected api line to show ready state, got: %s", apiLine)
	}
	if !strings.Contains(apiLine, "Yes") {
		t.Fatalf("expected api line to show ready=Yes, got: %s", apiLine)
	}
	if !strings.Contains(apiLine, "service ready") {
		t.Fatalf("expected api ready message, got: %s", apiLine)
	}

	dbLine := findServiceLine(output, "db")
	if !strings.Contains(dbLine, "Ready") {
		t.Fatalf("expected db to remain ready, got: %s", dbLine)
	}
}

func runStatusCommand(t *testing.T, ctx *context) string {
	t.Helper()
	cmd := newStatusCmd(ctx)
	cmd.SetContext(stdcontext.Background())
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status command failed: %v\nstderr: %s", err, stderr.String())
	}
	return stdout.String()
}

func findServiceLine(output, service string) string {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, service+" ") || trimmed == service {
			return trimmed
		}
	}
	return ""
}
