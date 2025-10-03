package cli

import (
	"bytes"
	stdcontext "context"
	"strings"
	"testing"
	"time"

	"github.com/example/orco/internal/engine"
	runtimelib "github.com/example/orco/internal/runtime"
)

func TestLogsCommandStreamsStructuredOutput(t *testing.T) {
	t.Parallel()

	rt := newMockRuntime()
	rt.logs["api"] = []runtimelib.LogEntry{{Message: "api ready", Source: runtimelib.LogSourceStdout}}
	rt.logs["worker"] = []runtimelib.LogEntry{{Message: "worker started", Source: runtimelib.LogSourceStdout}}

	stackPath := writeStackFile(t, `version: "0.1"
stack:
  name: "demo"
  workdir: "."
services:
  api:
    runtime: process
    command: ["sleep", "0"]
  worker:
    runtime: process
    command: ["sleep", "0"]
    dependsOn:
      - target: api
`)

	ctx := &context{
		stackFile:    &stackPath,
		orchestrator: engine.NewOrchestrator(runtimelib.Registry{"process": rt}),
	}

	cmd := newLogsCmd(ctx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"-f"})

	cmdCtx, cancel := stdcontext.WithCancel(stdcontext.Background())
	cmd.SetContext(cmdCtx)
        go func() {
                time.Sleep(150 * time.Millisecond)
                cancel()
        }()

	if err := cmd.Execute(); err != nil {
		t.Fatalf("logs command failed: %v\nstderr: %s", err, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "\"service\":\"api\"") {
		t.Fatalf("expected api logs in output, got: %s", output)
	}
	if !strings.Contains(output, "\"service\":\"worker\"") {
		t.Fatalf("expected worker logs in output, got: %s", output)
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr output, got: %s", stderr.String())
	}
}
