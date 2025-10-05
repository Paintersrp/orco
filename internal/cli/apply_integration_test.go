package cli

import (
	"bytes"
	stdcontext "context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/engine"
	"github.com/Paintersrp/orco/internal/runtime"
	"github.com/Paintersrp/orco/internal/stack"
)

func TestApplyCommandUpdatesService(t *testing.T) {
	t.Parallel()

	rt := newMockRuntime()

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
    replicas: 1
    command: ["sleep", "0"]
    env:
      FOO: bar
`)

	ctx := &context{
		stackFile:    &stackPath,
		orchestrator: engine.NewOrchestrator(runtime.Registry{"process": rt}),
	}

	events := make(chan engine.Event, 128)
	trackedEvents, release := ctx.trackEvents(events, cap(events))
	defer release()

	done := make(chan struct{})
	go func() {
		for range trackedEvents {
		}
		close(done)
	}()

	doc, err := ctx.loadStack()
	if err != nil {
		t.Fatalf("load stack: %v", err)
	}

	deployment, err := ctx.getOrchestrator().Up(stdcontext.Background(), doc.File, doc.Graph, events)
	if err != nil {
		t.Fatalf("orchestrator up: %v", err)
	}
	ctx.setDeployment(deployment, doc.File.Stack.Name, stack.CloneServiceMap(doc.File.Services))
	t.Cleanup(func() {
		stopCtx, cancel := stdcontext.WithTimeout(stdcontext.Background(), time.Second)
		_ = deployment.Stop(stopCtx, events)
		cancel()
		close(events)
		<-done
	})

	waitDeadline := time.Now().Add(2 * time.Second)
	for {
		snap := ctx.statusTracker().Snapshot()["api"]
		if snap.Ready && snap.ReadyReplicas >= 1 {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatalf("service did not report ready before apply: %+v", snap)
		}
		time.Sleep(10 * time.Millisecond)
	}

	updatedManifest := `version: "0.1"
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
    replicas: 1
    command: ["sleep", "1"]
    env:
      FOO: baz
`
	if err := os.WriteFile(stackPath, []byte(updatedManifest), 0o644); err != nil {
		t.Fatalf("update stack file: %v", err)
	}

	cmd := newApplyCmd(ctx)
	execCtx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 5*time.Second)
	defer cancel()
	cmd.SetContext(execCtx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("apply command failed: %v\nstderr: %s", err, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "Service api:") {
		t.Fatalf("expected diff output for service api, got:\n%s", output)
	}
	if !strings.Contains(output, "Updating api (1 replicas)") {
		t.Fatalf("expected update message in output, got:\n%s", output)
	}
	if !strings.Contains(output, "Service api ready (1/1 replicas") {
		t.Fatalf("expected readiness message in output, got:\n%s", output)
	}

	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr output, got: %s", stderr.String())
	}

	spec := ctx.currentDeploymentSpec()
	apiSpec := spec["api"]
	if apiSpec == nil {
		t.Fatalf("expected api spec in deployment state")
	}
	if apiSpec.Command[1] != "1" {
		t.Fatalf("expected command to be updated, got: %v", apiSpec.Command)
	}
	if apiSpec.Env["FOO"] != "baz" {
		t.Fatalf("expected env to be updated, got: %v", apiSpec.Env)
	}
}

func TestApplyCommandRollbackOnFailure(t *testing.T) {
	t.Parallel()

	rt := newMockRuntime()

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
    replicas: 1
    command: ["sleep", "0"]
    env:
      FOO: bar
`)

	ctx := &context{
		stackFile:    &stackPath,
		orchestrator: engine.NewOrchestrator(runtime.Registry{"process": rt}),
	}

	events := make(chan engine.Event, 128)
	trackedEvents, release := ctx.trackEvents(events, cap(events))
	defer release()

	done := make(chan struct{})
	go func() {
		for range trackedEvents {
		}
		close(done)
	}()

	doc, err := ctx.loadStack()
	if err != nil {
		t.Fatalf("load stack: %v", err)
	}

	deployment, err := ctx.getOrchestrator().Up(stdcontext.Background(), doc.File, doc.Graph, events)
	if err != nil {
		t.Fatalf("orchestrator up: %v", err)
	}
	ctx.setDeployment(deployment, doc.File.Stack.Name, stack.CloneServiceMap(doc.File.Services))
	t.Cleanup(func() {
		stopCtx, cancel := stdcontext.WithTimeout(stdcontext.Background(), time.Second)
		_ = deployment.Stop(stopCtx, events)
		cancel()
		close(events)
		<-done
	})

	waitDeadline := time.Now().Add(2 * time.Second)
	for {
		snap := ctx.statusTracker().Snapshot()["api"]
		if snap.Ready && snap.ReadyReplicas >= 1 {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatalf("service did not report ready before apply: %+v", snap)
		}
		time.Sleep(10 * time.Millisecond)
	}

	updatedManifest := `version: "0.1"
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
    replicas: 1
    command: ["sleep", "1"]
`
	if err := os.WriteFile(stackPath, []byte(updatedManifest), 0o644); err != nil {
		t.Fatalf("update stack file: %v", err)
	}

	rt.FailNextReady("api", errors.New("readiness failed"))

	cmd := newApplyCmd(ctx)
	execCtx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 5*time.Second)
	defer cancel()
	cmd.SetContext(execCtx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err == nil {
		t.Fatalf("expected apply command to fail")
	}

	if !strings.Contains(stdout.String(), "Updating api (1 replicas)") {
		t.Fatalf("expected update attempt in output, got:\n%s", stdout.String())
	}

	spec := ctx.currentDeploymentSpec()
	apiSpec := spec["api"]
	if apiSpec == nil {
		t.Fatalf("expected api spec in deployment state after rollback")
	}
	if len(apiSpec.Command) < 2 || apiSpec.Command[1] != "0" {
		t.Fatalf("expected rollback to restore original command, got: %v", apiSpec.Command)
	}
}
