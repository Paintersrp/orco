package cli

import (
        "bytes"
        "encoding/json"
        stdcontext "context"
        "strings"
        "testing"
        "time"

        "github.com/Paintersrp/orco/internal/api"
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
    resources:
      cpu: "500m"
      memory: "256Mi"
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
	if !strings.Contains(output, "CPU/MEM") {
		t.Fatalf("expected resource column in output, got: %s", output)
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
	if !strings.Contains(apiLine, "1/1") {
		t.Fatalf("expected api line to include replica readiness, got: %s", apiLine)
	}
	if !strings.Contains(apiLine, "service ready") {
		t.Fatalf("expected api ready message, got: %s", apiLine)
	}
	if !strings.Contains(apiLine, "500m / 256MiB") {
		t.Fatalf("expected api resources in output, got: %s", apiLine)
	}

	dbLine := findServiceLine(output, "db")
	if !strings.Contains(dbLine, "Ready") {
		t.Fatalf("expected db to remain ready, got: %s", dbLine)
	}
	if !strings.Contains(dbLine, "1/1") {
		t.Fatalf("expected db line to include replica readiness, got: %s", dbLine)
	}
	if !strings.Contains(dbLine, "-") {
		t.Fatalf("expected db resources placeholder in output, got: %s", dbLine)
	}
}

func runStatusCommand(t *testing.T, ctx *context, args ...string) string {
	t.Helper()
	cmd := newStatusCmd(ctx)
	cmd.SetContext(stdcontext.Background())
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
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

func TestStatusCommandHistoryFlag(t *testing.T) {
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
`)

	ctx := &context{stackFile: &stackPath}
	ctx.tracker = newStatusTracker(WithHistorySize(5))
	tracker := ctx.statusTracker()

	base := time.Now().Add(-2 * time.Minute)
	start := base
	ready := base.Add(10 * time.Second)
	tracker.Apply(engine.Event{Service: "api", Type: engine.EventTypeStarting, Message: "launch", Timestamp: start, Reason: engine.ReasonInitialStart})
	tracker.Apply(engine.Event{Service: "api", Type: engine.EventTypeReady, Message: "online", Timestamp: ready, Reason: engine.ReasonProbeReady})

	output := runStatusCommand(t, ctx, "--history", "5")
	if !strings.Contains(output, "api history:") {
		t.Fatalf("expected history section for api, got: %s", output)
	}
	if strings.Contains(output, "db history:") {
		t.Fatalf("did not expect history for undefined service, got: %s", output)
	}
	if !strings.Contains(output, start.Format(time.RFC3339)) {
		t.Fatalf("expected starting timestamp in history output: %s", output)
	}
	if !strings.Contains(output, ready.Format(time.RFC3339)) {
		t.Fatalf("expected ready timestamp in history output: %s", output)
	}
	if !strings.Contains(output, engine.ReasonInitialStart) {
		t.Fatalf("expected reason in history output: %s", output)
	}
	if !strings.Contains(output, engine.ReasonProbeReady) {
		t.Fatalf("expected second reason in history output: %s", output)
	}
        if !strings.Contains(output, "launch") || !strings.Contains(output, "online") {
                t.Fatalf("expected messages in history output: %s", output)
        }
}

func TestStatusCommandJSONOutput(t *testing.T) {
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

        base := time.Now().Add(-3 * time.Minute)
        tracker.Apply(engine.Event{Service: "api", Type: engine.EventTypeStarting, Timestamp: base, Reason: engine.ReasonInitialStart})
        tracker.Apply(engine.Event{Service: "api", Type: engine.EventTypeReady, Timestamp: base.Add(20 * time.Second), Reason: engine.ReasonProbeReady})
        tracker.Apply(engine.Event{Service: "db", Type: engine.EventTypeReady, Timestamp: base.Add(10 * time.Second), Reason: engine.ReasonProbeReady})

        cmd := newStatusCmd(ctx)
        cmd.SetContext(stdcontext.Background())
        var stdout, stderr bytes.Buffer
        cmd.SetOut(&stdout)
        cmd.SetErr(&stderr)
        cmd.SetArgs([]string{"--output", "json", "--history", "5"})

        if err := cmd.Execute(); err != nil {
                t.Fatalf("status command failed: %v\nstderr: %s", err, stderr.String())
        }

        var report api.StatusReport
        if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
                t.Fatalf("failed to decode json output: %v\noutput: %s", err, stdout.String())
        }

        if report.Stack != "demo" {
                t.Fatalf("expected stack name demo, got %s", report.Stack)
        }
        if report.Version != "0.1" {
                t.Fatalf("expected version 0.1, got %s", report.Version)
        }
        if report.GeneratedAt.IsZero() {
                t.Fatalf("expected GeneratedAt to be populated")
        }

        apiReport, ok := report.Services["api"]
        if !ok {
                t.Fatalf("expected api service in report, got: %#v", report.Services)
        }
        if apiReport.State != engine.EventTypeReady {
                t.Fatalf("expected api state ready, got %s", apiReport.State)
        }
        if apiReport.LastReason != engine.ReasonProbeReady {
                t.Fatalf("expected api last reason %s, got %s", engine.ReasonProbeReady, apiReport.LastReason)
        }
        if len(apiReport.History) == 0 {
                t.Fatalf("expected api history to be populated")
        }

        dbReport, ok := report.Services["db"]
        if !ok {
                t.Fatalf("expected db service in report, got: %#v", report.Services)
        }
        if dbReport.State != engine.EventTypeReady {
                t.Fatalf("expected db state ready, got %s", dbReport.State)
        }
        if dbReport.LastReason != engine.ReasonProbeReady {
                t.Fatalf("expected db last reason %s, got %s", engine.ReasonProbeReady, dbReport.LastReason)
        }
}
