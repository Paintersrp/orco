package cli

import (
	"bytes"
	stdcontext "context"
	"strings"
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/engine"
)

func TestGraphCommandRendersTreeWithStatusesAndAnnotations(t *testing.T) {
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
    command: ["sleep", "1"]
    dependsOn:
      - target: db
      - target: cache
        require: started
        timeout: 15s
  worker:
    runtime: process
    command: ["sleep", "1"]
    dependsOn:
      - target: db
        require: exists
  db:
    runtime: process
    command: ["sleep", "1"]
    dependsOn:
      - target: storage
        require: exists
  storage:
    runtime: process
    command: ["sleep", "1"]
  cache:
    runtime: process
    command: ["sleep", "1"]
    dependsOn:
      - target: redis
        require: ready
  redis:
    runtime: process
    command: ["sleep", "1"]
`)

	ctx := &context{stackFile: &stackPath}
	tracker := ctx.statusTracker()

	base := time.Now()
	tracker.Apply(engine.Event{Service: "storage", Type: engine.EventTypeReady, Timestamp: base})
	tracker.Apply(engine.Event{Service: "db", Type: engine.EventTypeReady, Timestamp: base.Add(1 * time.Second)})
	tracker.Apply(engine.Event{Service: "redis", Type: engine.EventTypeReady, Timestamp: base.Add(2 * time.Second)})
	tracker.Apply(engine.Event{Service: "worker", Type: engine.EventTypeReady, Timestamp: base.Add(3 * time.Second)})
	tracker.Apply(engine.Event{Service: "api", Type: engine.EventTypeReady, Timestamp: base.Add(4 * time.Second)})
	tracker.Apply(engine.Event{Service: "cache", Type: engine.EventTypeBlocked, Message: "waiting for redis", Replica: -1, Timestamp: base.Add(5 * time.Second)})

	cmd := newGraphCmd(ctx)
	cmd.SetContext(stdcontext.Background())
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(nil)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("graph command failed: %v\nstderr: %s", err, stderr.String())
	}

	output := stdout.String()

	expectations := []string{
		"api [Ready]",
		"├─ db (require=ready) [Ready]",
		"│  └─ storage (require=exists) [Ready]",
		"└─ cache (require=started, timeout=15s) [Blocked: waiting for redis]",
		"   └─ redis (require=ready) [Ready]",
		"worker [Ready]",
		"└─ db (require=exists) [Ready]",
	}

	for _, expected := range expectations {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected output to contain %q, got:\n%s", expected, output)
		}
	}
}

func TestGraphCommandDOTIncludesStatusStyling(t *testing.T) {
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
    command: ["sleep", "1"]
    dependsOn:
      - target: db
  db:
    runtime: process
    command: ["sleep", "1"]
`)

	ctx := &context{stackFile: &stackPath}
	tracker := ctx.statusTracker()

	base := time.Now()
	tracker.Apply(engine.Event{Service: "db", Type: engine.EventTypeReady, Replica: 0, Timestamp: base})
	tracker.Apply(engine.Event{Service: "api", Type: engine.EventTypeBlocked, Message: "blocked waiting for db (ready)", Replica: -1, Timestamp: base.Add(time.Second)})

	cmd := newGraphCmd(ctx)
	cmd.SetArgs([]string{"--dot"})

	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("graph command failed: %v\nstderr: %s", err, stderr.String())
	}

	output := stdout.String()

	expectations := []string{
		"\"api\" [label=\"api\\nBlocked\\nblocked waiting for db (ready)\" style=\"filled\" fillcolor=\"#fefcbf\"];",
		"\"db\" [label=\"db\\nReady\" style=\"filled\" fillcolor=\"#c6f6d5\"];",
		"\"api\" -> \"db\" [label=\"require=ready\\nblocked waiting for db (ready)\"];",
	}

	for _, expected := range expectations {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected DOT output to contain %q, got:\n%s", expected, output)
		}
	}
}
