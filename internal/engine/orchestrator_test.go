package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	runtimelib "github.com/example/orco/internal/runtime"
	"github.com/example/orco/internal/stack"
)

func TestOrchestratorGatesOnStartedRequirement(t *testing.T) {
	dbInstance := &fakeInstance{waitCh: make(chan error, 1)}
	apiInstance := &fakeInstance{waitCh: make(chan error, 1)}

	rt := newRecordingRuntime(map[string]*fakeInstance{
		"db":  dbInstance,
		"api": apiInstance,
	})

	doc := &stack.StackFile{
		Services: map[string]*stack.Service{
			"db": {
				Runtime: "test",
			},
			"api": {
				Runtime: "test",
				DependsOn: []stack.Dependency{{
					Target:  "db",
					Require: "started",
				}},
			},
		},
	}

	graph, err := BuildGraph(doc)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}

	orch := NewOrchestrator(runtimelib.Registry{"test": rt})

	events := make(chan Event, 32)
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for range events {
		}
	}()
	defer func() {
		close(events)
		<-drainDone
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resultCh := make(chan error, 1)
	var deployment *Deployment
	go func() {
		var upErr error
		deployment, upErr = orch.Up(ctx, doc, graph, events)
		resultCh <- upErr
	}()

	if name := waitForServiceStart(t, rt.startCh); name != "db" {
		t.Fatalf("expected db to start first, got %s", name)
	}

	select {
	case name := <-rt.startCh:
		if name != "api" {
			t.Fatalf("expected api start, got %s", name)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("api did not start once dependency reached started state")
	}

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("orchestrator returned error before readiness: %v", err)
		}
		t.Fatalf("orchestrator completed before services became ready")
	default:
	}

	dbInstance.waitCh <- nil
	apiInstance.waitCh <- nil

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("orchestrator up failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for orchestrator to finish")
	}

	if deployment == nil {
		t.Fatalf("expected deployment returned")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := deployment.Stop(stopCtx, events); err != nil {
		t.Fatalf("deployment stop: %v", err)
	}
}

func TestOrchestratorBlocksOnReadyRequirement(t *testing.T) {
	dbInstance := &fakeInstance{waitCh: make(chan error, 1)}
	apiInstance := &fakeInstance{waitCh: make(chan error, 1)}

	rt := newRecordingRuntime(map[string]*fakeInstance{
		"db":  dbInstance,
		"api": apiInstance,
	})

	doc := &stack.StackFile{
		Services: map[string]*stack.Service{
			"db": {
				Runtime: "test",
			},
			"api": {
				Runtime: "test",
				DependsOn: []stack.Dependency{{
					Target:  "db",
					Require: "ready",
				}},
			},
		},
	}

	graph, err := BuildGraph(doc)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}

	orch := NewOrchestrator(runtimelib.Registry{"test": rt})

	events := make(chan Event, 32)
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for range events {
		}
	}()
	defer func() {
		close(events)
		<-drainDone
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resultCh := make(chan error, 1)
	var deployment *Deployment
	go func() {
		var upErr error
		deployment, upErr = orch.Up(ctx, doc, graph, events)
		resultCh <- upErr
	}()

	if name := waitForServiceStart(t, rt.startCh); name != "db" {
		t.Fatalf("expected db to start first, got %s", name)
	}

	select {
	case name := <-rt.startCh:
		t.Fatalf("unexpected start for %s before dependency ready", name)
	case <-time.After(200 * time.Millisecond):
	}

	dbInstance.waitCh <- nil

	if name := waitForServiceStart(t, rt.startCh); name != "api" {
		t.Fatalf("expected api start after dependency readiness, got %s", name)
	}

	apiInstance.waitCh <- nil

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("orchestrator up failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for orchestrator to finish")
	}

	if deployment == nil {
		t.Fatalf("expected deployment returned")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := deployment.Stop(stopCtx, events); err != nil {
		t.Fatalf("deployment stop: %v", err)
	}
}

func TestOrchestratorDependencyFailureBlocksDependents(t *testing.T) {
	failure := errors.New("db never became ready")
	dbInstance := &fakeInstance{waitErr: failure}
	apiInstance := &fakeInstance{waitCh: make(chan error, 1)}

	rt := newRecordingRuntime(map[string]*fakeInstance{
		"db":  dbInstance,
		"api": apiInstance,
	})

	doc := &stack.StackFile{
		Services: map[string]*stack.Service{
			"db": {
				Runtime: "test",
			},
			"api": {
				Runtime: "test",
				DependsOn: []stack.Dependency{{
					Target:  "db",
					Require: "ready",
				}},
			},
		},
	}

	graph, err := BuildGraph(doc)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	orch := NewOrchestrator(runtimelib.Registry{"test": rt})

	events := make(chan Event, 32)
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for range events {
		}
	}()
	defer func() {
		close(events)
		<-drainDone
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	deployment, err := orch.Up(ctx, doc, graph, events)
	if err == nil {
		t.Fatalf("expected orchestrator up to fail")
	}
	if deployment != nil {
		t.Fatalf("expected nil deployment on failure")
	}

	if name := waitForServiceStart(t, rt.startCh); name != "db" {
		t.Fatalf("expected db to start first, got %s", name)
	}
	drainDeadline := time.After(100 * time.Millisecond)
	for {
		select {
		case name := <-rt.startCh:
			if name == "api" {
				t.Fatalf("api should not have started when dependency failed")
			}
		case <-drainDeadline:
			goto verify
		}
	}

verify:
	if got := err.Error(); !strings.Contains(got, "service api blocked waiting for db (ready)") {
		t.Fatalf("unexpected error message: %v", got)
	}
}

type recordingRuntime struct {
	mu        sync.Mutex
	instances map[string]*fakeInstance
	startCh   chan string
}

func newRecordingRuntime(instances map[string]*fakeInstance) *recordingRuntime {
	return &recordingRuntime{
		instances: instances,
		startCh:   make(chan string, 32),
	}
}

func (r *recordingRuntime) Start(ctx context.Context, spec runtimelib.StartSpec) (runtimelib.Handle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := spec.Name
	inst, ok := r.instances[name]
	if !ok {
		return nil, fmt.Errorf("no instance configured for service %s", name)
	}
	if r.startCh != nil {
		r.startCh <- name
	}
	return inst, nil
}

func waitForServiceStart(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case name := <-ch:
		return name
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for runtime start")
		return ""
	}
}
