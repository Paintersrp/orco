package cli

import (
        "bytes"
        "encoding/json"
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
        cmd.SetArgs([]string{"--output", "dot"})

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

func TestGraphCommandJSONOutput(t *testing.T) {
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
        timeout: 30s
  db:
    runtime: process
    command: ["sleep", "1"]
`)

        ctx := &context{stackFile: &stackPath}
        tracker := ctx.statusTracker()

        base := time.Now()
        tracker.Apply(engine.Event{Service: "db", Type: engine.EventTypeReady, Timestamp: base})
        tracker.Apply(engine.Event{Service: "api", Type: engine.EventTypeBlocked, Message: "waiting for db", Timestamp: base.Add(5 * time.Second)})

        cmd := newGraphCmd(ctx)
        cmd.SetArgs([]string{"--output", "json"})

        var stdout, stderr bytes.Buffer
        cmd.SetOut(&stdout)
        cmd.SetErr(&stderr)

        if err := cmd.Execute(); err != nil {
                t.Fatalf("graph command failed: %v\nstderr: %s", err, stderr.String())
        }

        var payload struct {
                Stack   string `json:"stack"`
                Version string `json:"version"`
                Nodes   []struct {
                        Name    string           `json:"name"`
                        State   engine.EventType `json:"state"`
                        Ready   bool             `json:"ready"`
                        Message string           `json:"message"`
                } `json:"nodes"`
                Edges []struct {
                        From           string `json:"from"`
                        To             string `json:"to"`
                        Require        string `json:"require"`
                        Timeout        string `json:"timeout"`
                        BlockingReason string `json:"blockingReason"`
                } `json:"edges"`
        }

        if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
                t.Fatalf("failed to parse json output: %v\noutput: %s", err, stdout.String())
        }

        if payload.Stack != "demo" {
                t.Fatalf("expected stack demo, got %s", payload.Stack)
        }
        if payload.Version != "0.1" {
                t.Fatalf("expected version 0.1, got %s", payload.Version)
        }

        if len(payload.Nodes) < 2 {
                t.Fatalf("expected at least two nodes, got %d", len(payload.Nodes))
        }

        var apiNodeFound, edgeFound bool
        for _, node := range payload.Nodes {
                if node.Name == "api" {
                        apiNodeFound = true
                        if node.State != engine.EventTypeBlocked {
                                t.Fatalf("expected api state blocked, got %s", node.State)
                        }
                        if node.Message != "waiting for db" {
                                t.Fatalf("expected api message to match, got %s", node.Message)
                        }
                }
        }
        if !apiNodeFound {
                t.Fatalf("expected api node in payload: %#v", payload.Nodes)
        }

        for _, edge := range payload.Edges {
                if edge.From == "api" && edge.To == "db" {
                        edgeFound = true
                        if edge.Require != "ready" {
                                t.Fatalf("expected require metadata 'ready', got %s", edge.Require)
                        }
                        if edge.Timeout != "30s" {
                                t.Fatalf("expected timeout metadata '30s', got %s", edge.Timeout)
                        }
                        if edge.BlockingReason != "waiting for db" {
                                t.Fatalf("expected blocking reason to match, got %s", edge.BlockingReason)
                        }
                }
        }
        if !edgeFound {
                t.Fatalf("expected dependency edge from api to db: %#v", payload.Edges)
        }
}
