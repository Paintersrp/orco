package cli

import (
	"bytes"
	stdcontext "context"
	"strings"
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/engine"
	"github.com/Paintersrp/orco/internal/runtime"
	"github.com/Paintersrp/orco/internal/stack"
)

func TestRestartCommandPerReplicaSequence(t *testing.T) {
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
    replicas: 3
    command: ["sleep", "0"]
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
		if snap.Ready && snap.ReadyReplicas == 3 {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatalf("service did not report ready before restart: %+v", snap)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cmd := newRestartCmd(ctx)
	execCtx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 5*time.Second)
	defer cancel()
	cmd.SetContext(execCtx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"api"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("restart command failed: %v\nstderr: %s", err, stderr.String())
	}

	output := stdout.String()
	expectations := []string{
		"Rolling restart of api (3 replicas)",
		"Restarting api replica 1/3",
		"Replica 1/3 ready (service readiness: 3/3, Ready)",
		"Restarting api replica 2/3",
		"Replica 2/3 ready (service readiness: 3/3, Ready)",
		"Restarting api replica 3/3",
		"Replica 3/3 ready (service readiness: 3/3, Ready)",
		"Completed rolling restart of api.",
	}
	for _, want := range expectations {
		if !strings.Contains(output, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, output)
		}
	}

	stops := rt.stopOrder()
	if len(stops) != 3 {
		t.Fatalf("expected three stop invocations, got %d", len(stops))
	}

	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr output, got: %s", stderr.String())
	}
}

func TestRestartServiceHandleSequential(t *testing.T) {
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
    replicas: 3
    command: ["sleep", "0"]
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
		if snap.Ready && snap.ReadyReplicas == 3 {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatalf("service did not report ready before restart: %+v", snap)
		}
		time.Sleep(10 * time.Millisecond)
	}

	service, ok := deployment.Service("api")
	if !ok {
		t.Fatalf("expected deployment to expose service handle")
	}

	for i := 0; i < service.Replicas(); i++ {
		stepCtx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 5*time.Second)
		if err := service.RestartReplica(stepCtx, i); err != nil {
			cancel()
			t.Fatalf("restart replica %d: %v", i, err)
		}
		cancel()

		deadline := time.Now().Add(5 * time.Second)
		for {
			snap := ctx.statusTracker().Snapshot()["api"]
			if snap.Ready && snap.ReadyReplicas == service.Replicas() {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("service not ready after restarting replica %d: %+v", i, snap)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}
