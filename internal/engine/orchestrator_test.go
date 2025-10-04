package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	runtimelib "github.com/Paintersrp/orco/internal/runtime"
	"github.com/Paintersrp/orco/internal/stack"
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
	t.Parallel()

	cases := map[string]struct {
		configureDependency func(dep *stack.Dependency)
		configureInstance   func(db *fakeInstance)
		wantReasonContains  string
	}{
		"readiness failure": {
			configureDependency: func(dep *stack.Dependency) {},
			configureInstance: func(db *fakeInstance) {
				db.waitErr = errors.New("db never became ready")
			},
			wantReasonContains: "db never became ready",
		},
		"dependency timeout": {
			configureDependency: func(dep *stack.Dependency) {
				dep.Timeout.Duration = 50 * time.Millisecond
			},
			configureInstance: func(db *fakeInstance) {
				db.waitCh = make(chan error)
			},
			wantReasonContains: context.DeadlineExceeded.Error(),
		},
	}

	for name, tc := range cases {
		name := name
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dbInstance := &fakeInstance{}
			tc.configureInstance(dbInstance)
			apiInstance := &fakeInstance{waitCh: make(chan error, 1)}

			rt := newRecordingRuntime(map[string]*fakeInstance{
				"db":  dbInstance,
				"api": apiInstance,
			})

			dep := stack.Dependency{
				Target:  "db",
				Require: "ready",
			}
			tc.configureDependency(&dep)

			doc := &stack.StackFile{
				Services: map[string]*stack.Service{
					"db": {
						Runtime: "test",
					},
					"api": {
						Runtime:   "test",
						DependsOn: []stack.Dependency{dep},
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
			var (
				mu       sync.Mutex
				recorded []Event
			)
			go func() {
				defer close(drainDone)
				for evt := range events {
					mu.Lock()
					recorded = append(recorded, evt)
					mu.Unlock()
				}
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

			close(events)
			<-drainDone

			mu.Lock()
			recordedCopy := append([]Event(nil), recorded...)
			mu.Unlock()

			var blocked *Event
			for i := range recordedCopy {
				evt := recordedCopy[i]
				if evt.Service == "api" && evt.Type == EventTypeBlocked {
					blocked = &evt
					break
				}
			}

			if blocked == nil {
				t.Fatalf("missing blocked event for api service; got %+v", recordedCopy)
			}

			if !strings.Contains(blocked.Message, "blocked waiting for db (ready)") {
				t.Fatalf("unexpected blocked message: %+v", blocked)
			}

			if blocked.Err == nil {
				t.Fatalf("blocked event missing error: %+v", blocked)
			}

			if !strings.Contains(blocked.Reason, tc.wantReasonContains) {
				t.Fatalf("blocked reason %q missing %q", blocked.Reason, tc.wantReasonContains)
			}

			if !strings.Contains(blocked.Err.Error(), tc.wantReasonContains) {
				t.Fatalf("blocked error %q missing %q", blocked.Err.Error(), tc.wantReasonContains)
			}
		})
	}
}

func TestOrchestratorEmitsLifecycleMetadata(t *testing.T) {
	instance := &fakeInstance{waitCh: make(chan error, 1)}
	rt := newRecordingRuntime(map[string]*fakeInstance{
		"app": instance,
	})

	doc := &stack.StackFile{
		Services: map[string]*stack.Service{
			"app": {Runtime: "test"},
		},
	}

	graph, err := BuildGraph(doc)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}

	orch := NewOrchestrator(runtimelib.Registry{"test": rt})

	events := make(chan Event, 32)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resultCh := make(chan error, 1)
	var deployment *Deployment
	go func() {
		var upErr error
		deployment, upErr = orch.Up(ctx, doc, graph, events)
		resultCh <- upErr
	}()

	waitForServiceStart(t, rt.startCh)

	instance.waitCh <- nil

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("orchestrator up failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for orchestrator to finish")
	}

	recorded := make([]Event, 0, 4)
	startupDeadline := time.After(time.Second)
	for len(recorded) < 2 {
		select {
		case evt := <-events:
			recorded = append(recorded, evt)
		case <-startupDeadline:
			t.Fatalf("timed out waiting for startup events; got %v", recorded)
		}
	}

	var starting Event
	var ready Event
	foundStart := false
	foundReady := false
	for _, evt := range recorded {
		if evt.Service != "app" {
			continue
		}
		switch evt.Type {
		case EventTypeStarting:
			if !foundStart {
				starting = evt
				foundStart = true
			}
		case EventTypeReady:
			if !foundReady {
				ready = evt
				foundReady = true
			}
		}
	}

	if !foundStart {
		t.Fatalf("missing starting event")
	}
	if starting.Attempt != 1 || starting.Reason != ReasonInitialStart {
		t.Fatalf("starting metadata mismatch: %+v", starting)
	}

	if !foundReady {
		t.Fatalf("missing ready event")
	}
	if ready.Attempt != 1 || ready.Reason != ReasonProbeReady {
		t.Fatalf("ready metadata mismatch: %+v", ready)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()

	if err := deployment.Stop(stopCtx, events); err != nil {
		t.Fatalf("deployment stop: %v", err)
	}

	var stopping Event
	var stopped Event
	foundStopping := false
	foundStopped := false
	stopDeadline := time.After(time.Second)
stopLoop:
	for {
		if foundStopping && foundStopped {
			break stopLoop
		}
		select {
		case evt := <-events:
			recorded = append(recorded, evt)
			if evt.Service != "app" {
				continue
			}
			switch evt.Type {
			case EventTypeStopping:
				if !foundStopping {
					stopping = evt
					foundStopping = true
				}
			case EventTypeStopped:
				if !foundStopped {
					stopped = evt
					foundStopped = true
				}
			}
		case <-stopDeadline:
			break stopLoop
		}
	}

	if !foundStopping {
		t.Fatalf("missing stopping event")
	}
	if stopping.Attempt != 0 || stopping.Reason != ReasonShutdown {
		t.Fatalf("stopping metadata mismatch: %+v", stopping)
	}

	if !foundStopped {
		t.Fatalf("missing stopped event")
	}
	if stopped.Attempt != 1 || stopped.Reason != ReasonSupervisorStop {
		t.Fatalf("stopped metadata mismatch: %+v", stopped)
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
