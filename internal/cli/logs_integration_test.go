package cli

import (
	"bytes"
	stdcontext "context"
	"strings"
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/engine"
	runtimelib "github.com/Paintersrp/orco/internal/runtime"
	"github.com/Paintersrp/orco/internal/stack"
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
defaults:
  health:
    cmd:
      command: ["true"]
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

	startDeployment(t, ctx)

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

func TestLogsCommandSinceFiltersOldEntries(t *testing.T) {
	t.Parallel()

	rt := newMockRuntime()
	rt.logs["api"] = []runtimelib.LogEntry{
		{Message: "too old", Source: runtimelib.LogSourceStdout, Timestamp: time.Now().Add(-2 * time.Minute)},
		{Message: "still relevant", Source: runtimelib.LogSourceStdout, Timestamp: time.Now().Add(-30 * time.Second)},
	}

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

	ctx := &context{
		stackFile:    &stackPath,
		orchestrator: engine.NewOrchestrator(runtimelib.Registry{"process": rt}),
	}

	startDeployment(t, ctx)

	cmd := newLogsCmd(ctx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--since", "1m", "-f"})

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
	if strings.Contains(output, "too old") {
		t.Fatalf("expected old log entries to be filtered out, got: %s", output)
	}
	if !strings.Contains(output, "still relevant") {
		t.Fatalf("expected recent log entry to be present, got: %s", output)
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr output, got: %s", stderr.String())
	}
}

func TestLogsCommandDoesNotRestartServices(t *testing.T) {
	t.Parallel()

	rt := newMockRuntime()
	rt.logs["api"] = []runtimelib.LogEntry{{Message: "api ready", Source: runtimelib.LogSourceStdout}}

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

	ctx := &context{
		stackFile:    &stackPath,
		orchestrator: engine.NewOrchestrator(runtimelib.Registry{"process": rt}),
	}

	startDeployment(t, ctx)

	before := append([]string(nil), rt.startOrder()...)

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

	if !strings.Contains(stdout.String(), "\"service\":\"api\"") {
		t.Fatalf("expected api logs in output, got: %s", stdout.String())
	}

	after := rt.startOrder()
	if len(after) != len(before) {
		t.Fatalf("expected no additional service starts, before=%v after=%v", before, after)
	}
}

func startDeployment(t *testing.T, ctx *context) {
	t.Helper()

	doc, err := ctx.loadStack()
	if err != nil {
		t.Fatalf("load stack: %v", err)
	}

	events := make(chan engine.Event, 256)
	tracked, release := ctx.trackEvents(events, cap(events))
	drained := make(chan struct{})
	go func() {
		for range tracked {
		}
		close(drained)
	}()

	deployment, err := ctx.getOrchestrator().Up(stdcontext.Background(), doc.File, doc.Graph, events)
	if err != nil {
		t.Fatalf("start deployment: %v", err)
	}
	ctx.setDeployment(deployment, doc.File.Stack.Name, stack.CloneServiceMap(doc.File.Services))

	t.Cleanup(func() {
		stopCtx, cancel := stdcontext.WithTimeout(stdcontext.Background(), time.Second)
		_ = deployment.Stop(stopCtx, events)
		cancel()
		ctx.clearDeployment(deployment)
		release()
		close(events)
		<-drained
	})
}
