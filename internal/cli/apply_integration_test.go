package cli

import (
	"bytes"
	stdcontext "context"
	"errors"
	"os"
	"strings"
	"sync"
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

	doc, err := ctx.loadStack()
	if err != nil {
		t.Fatalf("load stack: %v", err)
	}

	events := make(chan engine.Event, 128)
	trackedEvents, release := ctx.trackEvents(doc.File.Stack.Name, events, cap(events))
	defer release()

	done := make(chan struct{})
	go func() {
		for range trackedEvents {
		}
		close(done)
	}()

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

	doc, err := ctx.loadStack()
	if err != nil {
		t.Fatalf("load stack: %v", err)
	}

	events := make(chan engine.Event, 128)
	trackedEvents, release := ctx.trackEvents(doc.File.Stack.Name, events, cap(events))
	defer release()

	done := make(chan struct{})
	go func() {
		for range trackedEvents {
		}
		close(done)
	}()

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

func TestApplyCommandWaitsForManualCanaryPromotion(t *testing.T) {
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
    replicas: 2
    command: ["sleep", "0"]
    env:
      VERSION: v1
    update:
      strategy: canary
`)

	ctx := &context{
		stackFile:    &stackPath,
		orchestrator: engine.NewOrchestrator(runtime.Registry{"process": rt}),
	}

	doc, err := ctx.loadStack()
	if err != nil {
		t.Fatalf("load stack: %v", err)
	}

	events := make(chan engine.Event, 256)
	trackedEvents, release := ctx.trackEvents(doc.File.Stack.Name, events, cap(events))
	defer release()

	var (
		mu       sync.Mutex
		recorded []engine.Event
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for evt := range trackedEvents {
			mu.Lock()
			recorded = append(recorded, evt)
			mu.Unlock()
		}
	}()

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

	waitDeadline := time.Now().Add(5 * time.Second)
	for {
		snap := ctx.statusTracker().Snapshot()["api"]
		if snap.Ready && snap.ReadyReplicas >= 2 {
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
    replicas: 2
    command: ["sleep", "1"]
    env:
      VERSION: v2
    update:
      strategy: canary
`
	if err := os.WriteFile(stackPath, []byte(updatedManifest), 0o644); err != nil {
		t.Fatalf("update stack file: %v", err)
	}

	applyCmd := newApplyCmd(ctx)
	applyCtx, cancelApply := stdcontext.WithTimeout(stdcontext.Background(), 10*time.Second)
	defer cancelApply()
	applyCmd.SetContext(applyCtx)
	var applyStdout, applyStderr bytes.Buffer
	applyCmd.SetOut(&applyStdout)
	applyCmd.SetErr(&applyStderr)

	applyDone := make(chan error, 1)
	go func() {
		applyDone <- applyCmd.Execute()
	}()

	canaryDeadline := time.Now().Add(5 * time.Second)
	for {
		snap := ctx.statusTracker().Snapshot()["api"]
		if snap.State == engine.EventTypeCanary {
			break
		}
		if time.Now().After(canaryDeadline) {
			t.Fatalf("service did not enter canary state: %+v", snap)
		}
		time.Sleep(10 * time.Millisecond)
	}

	promoteCmd := newPromoteCmd(ctx)
	promoteCtx, cancelPromote := stdcontext.WithTimeout(stdcontext.Background(), 5*time.Second)
	promoteCmd.SetContext(promoteCtx)
	promoteCmd.SetArgs([]string{"api"})
	var promoteStdout, promoteStderr bytes.Buffer
	promoteCmd.SetOut(&promoteStdout)
	promoteCmd.SetErr(&promoteStderr)

	if err := promoteCmd.Execute(); err != nil {
		cancelPromote()
		t.Fatalf("promote command failed: %v", err)
	}
	cancelPromote()

	select {
	case err := <-applyDone:
		if err != nil {
			t.Fatalf("apply command failed: %v\nstderr: %s", err, applyStderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for apply command to finish")
	}

	if applyStderr.Len() != 0 {
		t.Fatalf("expected no stderr output from apply, got: %s", applyStderr.String())
	}
	if promoteStderr.Len() != 0 {
		t.Fatalf("expected no stderr output from promote, got: %s", promoteStderr.String())
	}

	applyOutput := applyStdout.String()
	if !strings.Contains(applyOutput, "Service api canary ready") || !strings.Contains(applyOutput, "`orco promote api`") {
		t.Fatalf("expected canary prompt in apply output, got:\n%s", applyOutput)
	}
	if !strings.Contains(applyOutput, "Service api ready (2/2 replicas") {
		t.Fatalf("expected readiness message in apply output, got:\n%s", applyOutput)
	}

	promoteOutput := promoteStdout.String()
	if !strings.Contains(promoteOutput, "Promoting service api...") {
		t.Fatalf("expected promotion start message, got:\n%s", promoteOutput)
	}
	if !strings.Contains(promoteOutput, "Service api promotion complete.") {
		t.Fatalf("expected promotion completion message, got:\n%s", promoteOutput)
	}

	spec := ctx.currentDeploymentSpec()
	apiSpec := spec["api"]
	if apiSpec == nil {
		t.Fatalf("expected api spec after promotion")
	}
	if apiSpec.Env["VERSION"] != "v2" {
		t.Fatalf("expected env VERSION=v2 after promotion, got: %v", apiSpec.Env)
	}
	if apiSpec.Command[1] != "1" {
		t.Fatalf("expected command to be updated, got: %v", apiSpec.Command)
	}

	mu.Lock()
	recordedCopy := append([]engine.Event(nil), recorded...)
	mu.Unlock()
	promotedObserved := false
	for _, evt := range recordedCopy {
		if evt.Service == "api" && evt.Type == engine.EventTypePromoted {
			promotedObserved = true
			break
		}
	}
	if !promotedObserved {
		t.Fatalf("missing promoted event in engine stream: %+v", recordedCopy)
	}

	history := ctx.statusTracker().History("api", 5)
	historyPromoted := false
	for _, entry := range history {
		if entry.Type == engine.EventTypePromoted {
			historyPromoted = true
			break
		}
	}
	if !historyPromoted {
		t.Fatalf("expected promoted transition in tracker history, got: %+v", history)
	}
}
