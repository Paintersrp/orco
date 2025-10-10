package engine

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/probe"
	runtimelib "github.com/Paintersrp/orco/internal/runtime"
	"github.com/Paintersrp/orco/internal/stack"
)

func TestOrchestratorGatesOnStartedRequirement(t *testing.T) {
	dbInstance := &fakeInstance{waitCh: make(chan error, 1)}
	apiInstance := &fakeInstance{waitCh: make(chan error, 1)}

	rt := newRecordingRuntime(map[string][]*fakeInstance{
		"db":  []*fakeInstance{dbInstance},
		"api": []*fakeInstance{apiInstance},
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

	if name := waitForServiceStart(t, rt.startCh); name != "db[0]" {
		t.Fatalf("expected db to start first, got %s", name)
	}

	select {
	case name := <-rt.startCh:
		if name != "api[0]" {
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

	rt := newRecordingRuntime(map[string][]*fakeInstance{
		"db":  []*fakeInstance{dbInstance},
		"api": []*fakeInstance{apiInstance},
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

	if name := waitForServiceStart(t, rt.startCh); name != "db[0]" {
		t.Fatalf("expected db to start first, got %s", name)
	}

	select {
	case name := <-rt.startCh:
		t.Fatalf("unexpected start for %s before dependency ready", name)
	case <-time.After(200 * time.Millisecond):
	}

	dbInstance.waitCh <- nil

	if name := waitForServiceStart(t, rt.startCh); name != "api[0]" {
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

func TestOrchestratorWaitsForReplicaReadiness(t *testing.T) {
	db0 := &fakeInstance{waitCh: make(chan error, 1)}
	db1 := &fakeInstance{waitCh: make(chan error, 1)}
	apiInstance := &fakeInstance{waitCh: make(chan error, 1)}

	rt := newRecordingRuntime(map[string][]*fakeInstance{
		"db":  []*fakeInstance{db0, db1},
		"api": []*fakeInstance{apiInstance},
	})

	doc := &stack.StackFile{
		Services: map[string]*stack.Service{
			"db": {
				Runtime:  "test",
				Replicas: 2,
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

	events := make(chan Event, 64)
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
	closed := false
	defer func() {
		if !closed {
			close(events)
		}
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

	if name := waitForServiceStart(t, rt.startCh); name != "db[0]" {
		t.Fatalf("expected first db replica start, got %s", name)
	}
	if name := waitForServiceStart(t, rt.startCh); name != "db[1]" {
		t.Fatalf("expected second db replica start, got %s", name)
	}

	select {
	case name := <-rt.startCh:
		t.Fatalf("unexpected service start before readiness: %s", name)
	case <-time.After(200 * time.Millisecond):
	}

	db0.waitCh <- nil

	select {
	case name := <-rt.startCh:
		t.Fatalf("unexpected service start before all replicas ready: %s", name)
	case <-time.After(200 * time.Millisecond):
	}

	db1.waitCh <- nil

	if name := waitForServiceStart(t, rt.startCh); name != "api[0]" {
		t.Fatalf("expected api start after all replicas ready, got %s", name)
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
	close(events)
	closed = true
	<-drainDone

	mu.Lock()
	recordedCopy := append([]Event(nil), recorded...)
	mu.Unlock()

	var dbStops []int
	replicaStops := make(map[string]map[int]bool)
	for _, evt := range recordedCopy {
		if evt.Type != EventTypeStopping {
			continue
		}
		if replicaStops[evt.Service] == nil {
			replicaStops[evt.Service] = make(map[int]bool)
		}
		replicaStops[evt.Service][evt.Replica] = true
		if evt.Service == "db" {
			dbStops = append(dbStops, evt.Replica)
		}
	}
	if len(replicaStops["api"]) != 1 || !replicaStops["api"][0] {
		t.Fatalf("expected stop event for api replica 0, got %+v", replicaStops["api"])
	}
	if len(dbStops) != 2 || !replicaStops["db"][0] || !replicaStops["db"][1] {
		t.Fatalf("expected stop events for both db replicas, got %+v", replicaStops["db"])
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

			rt := newRecordingRuntime(map[string][]*fakeInstance{
				"db":  []*fakeInstance{dbInstance},
				"api": []*fakeInstance{apiInstance},
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

			if name := waitForServiceStart(t, rt.startCh); name != "db[0]" {
				t.Fatalf("expected db to start first, got %s", name)
			}
			drainDeadline := time.After(100 * time.Millisecond)
			for {
				select {
				case name := <-rt.startCh:
					if name == "api[0]" {
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
	rt := newRecordingRuntime(map[string][]*fakeInstance{
		"app": []*fakeInstance{instance},
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
	if starting.Attempt != 1 || starting.Reason != ReasonInitialStart || starting.Replica != 0 {
		t.Fatalf("starting metadata mismatch: %+v", starting)
	}

	if !foundReady {
		t.Fatalf("missing ready event")
	}
	if ready.Attempt != 1 || ready.Reason != ReasonProbeReady || ready.Replica != 0 {
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
	if stopping.Attempt != 0 || stopping.Reason != ReasonShutdown || stopping.Replica != 0 {
		t.Fatalf("stopping metadata mismatch: %+v", stopping)
	}

	if !foundStopped {
		t.Fatalf("missing stopped event")
	}
	if stopped.Attempt != 1 || stopped.Reason != ReasonSupervisorStop || stopped.Replica != 0 {
		t.Fatalf("stopped metadata mismatch: %+v", stopped)
	}
}

func TestServiceUpdateCanaryRequiresPromotion(t *testing.T) {
	apiInitial0 := &fakeInstance{waitCh: make(chan error, 1)}
	apiInitial1 := &fakeInstance{waitCh: make(chan error, 1)}
	apiUpdate0 := &fakeInstance{waitCh: make(chan error, 1)}
	apiUpdate1 := &fakeInstance{waitCh: make(chan error, 1)}

	rt := newRecordingRuntime(map[string][]*fakeInstance{
		"api": {apiInitial0, apiInitial1, apiUpdate0, apiUpdate1},
	})

	doc := &stack.StackFile{
		Stack: stack.StackMeta{Name: "demo"},
		Services: map[string]*stack.Service{
			"api": {
				Runtime:  "test",
				Replicas: 2,
			},
		},
	}

	graph, err := BuildGraph(doc)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}

	orch := NewOrchestrator(runtimelib.Registry{"test": rt})

	events := make(chan Event, 128)
	var (
		mu       sync.Mutex
		recorded []Event
	)
	drain := make(chan struct{})
	go func() {
		defer close(drain)
		for evt := range events {
			mu.Lock()
			recorded = append(recorded, evt)
			mu.Unlock()
		}
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

	if name := waitForServiceStart(t, rt.startCh); name != "api[0]" {
		t.Fatalf("expected first replica start, got %s", name)
	}
	if name := waitForServiceStart(t, rt.startCh); name != "api[1]" {
		t.Fatalf("expected second replica start, got %s", name)
	}

	apiInitial0.waitCh <- nil
	apiInitial1.waitCh <- nil

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

	service, ok := deployment.Service("api")
	if !ok {
		t.Fatalf("expected api service handle")
	}

	updateDone := make(chan error, 1)
	newSpec := &stack.Service{Runtime: "test", Replicas: 2, Command: []string{"sleep", "1"}}
	go func() {
		updateDone <- service.Update(context.Background(), newSpec)
	}()

	if name := waitForServiceStart(t, rt.startCh); name != "api[2]" {
		t.Fatalf("expected first replica restart, got %s", name)
	}

	apiUpdate0.waitCh <- nil

	select {
	case name := <-rt.startCh:
		t.Fatalf("unexpected restart before promotion: %s", name)
	case <-time.After(200 * time.Millisecond):
	}

	promoteDone := make(chan error, 1)
	go func() {
		promoteDone <- service.Promote(context.Background())
	}()

	if name := waitForServiceStart(t, rt.startCh); name != "api[3]" {
		t.Fatalf("expected second replica restart after promotion, got %s", name)
	}

	apiUpdate1.waitCh <- nil

	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf("service update failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for service update to complete")
	}

	select {
	case err := <-promoteDone:
		if err != nil {
			t.Fatalf("promotion failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for promotion to complete")
	}

	select {
	case name := <-rt.startCh:
		t.Fatalf("unexpected additional restart: %s", name)
	default:
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := deployment.Stop(stopCtx, events); err != nil {
		t.Fatalf("deployment stop: %v", err)
	}

	close(events)
	<-drain

	mu.Lock()
	recordedCopy := append([]Event(nil), recorded...)
	mu.Unlock()

	canaryObserved := false
	promotedObserved := false
	for _, evt := range recordedCopy {
		if evt.Service != "api" {
			continue
		}
		switch evt.Type {
		case EventTypeCanary:
			if evt.Replica == 0 && evt.Reason == ReasonCanary {
				canaryObserved = true
			}
		case EventTypePromoted:
			if evt.Replica == -1 && evt.Reason == ReasonPromoted {
				promotedObserved = true
			}
		case EventTypeAborted:
			t.Fatalf("unexpected aborted event: %+v", evt)
		}
	}

	if !canaryObserved {
		t.Fatalf("missing canary event for manual promotion: %+v", recordedCopy)
	}
	if !promotedObserved {
		t.Fatalf("missing promoted event for manual promotion: %+v", recordedCopy)
	}
}

func TestServiceUpdateAutoPromoteAfterDeadline(t *testing.T) {
	apiInitial0 := &fakeInstance{waitCh: make(chan error, 1)}
	apiInitial1 := &fakeInstance{waitCh: make(chan error, 1)}
	apiUpdate0 := &fakeInstance{waitCh: make(chan error, 1)}
	apiUpdate1 := &fakeInstance{waitCh: make(chan error, 1)}

	rt := newRecordingRuntime(map[string][]*fakeInstance{
		"api": {apiInitial0, apiInitial1, apiUpdate0, apiUpdate1},
	})

	doc := &stack.StackFile{
		Stack: stack.StackMeta{Name: "demo"},
		Services: map[string]*stack.Service{
			"api": {
				Runtime:  "test",
				Replicas: 2,
			},
		},
	}

	graph, err := BuildGraph(doc)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}

	orch := NewOrchestrator(runtimelib.Registry{"test": rt})

	events := make(chan Event, 128)
	var (
		mu       sync.Mutex
		recorded []Event
	)
	drain := make(chan struct{})
	go func() {
		defer close(drain)
		for evt := range events {
			mu.Lock()
			recorded = append(recorded, evt)
			mu.Unlock()
		}
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

	if name := waitForServiceStart(t, rt.startCh); name != "api[0]" {
		t.Fatalf("expected first replica start, got %s", name)
	}
	if name := waitForServiceStart(t, rt.startCh); name != "api[1]" {
		t.Fatalf("expected second replica start, got %s", name)
	}

	apiInitial0.waitCh <- nil
	apiInitial1.waitCh <- nil

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

	service, ok := deployment.Service("api")
	if !ok {
		t.Fatalf("expected api service handle")
	}

	updateDone := make(chan error, 1)
	newSpec := &stack.Service{
		Runtime:  "test",
		Replicas: 2,
		Command:  []string{"sleep", "1"},
		Update: &stack.UpdatePolicy{
			PromoteAfter: stack.Duration{Duration: 50 * time.Millisecond},
		},
	}

	go func() {
		updateDone <- service.Update(context.Background(), newSpec)
	}()

	if name := waitForServiceStart(t, rt.startCh); name != "api[2]" {
		t.Fatalf("expected first replica restart, got %s", name)
	}

	apiUpdate0.waitCh <- nil

	if name := waitForServiceStart(t, rt.startCh); name != "api[3]" {
		t.Fatalf("expected autopromotion to restart second replica, got %s", name)
	}

	apiUpdate1.waitCh <- nil

	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf("service update failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for service update to complete")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := deployment.Stop(stopCtx, events); err != nil {
		t.Fatalf("deployment stop: %v", err)
	}

	close(events)
	<-drain

	mu.Lock()
	recordedCopy := append([]Event(nil), recorded...)
	mu.Unlock()

	canaryObserved := false
	promotedObserved := false
	for _, evt := range recordedCopy {
		if evt.Service != "api" {
			continue
		}
		switch evt.Type {
		case EventTypeCanary:
			if evt.Replica == 0 && evt.Reason == ReasonCanary {
				canaryObserved = true
			}
		case EventTypePromoted:
			if evt.Replica == -1 && evt.Reason == ReasonPromoted {
				promotedObserved = true
			}
		case EventTypeAborted:
			t.Fatalf("unexpected aborted event: %+v", evt)
		}
	}

	if !canaryObserved {
		t.Fatalf("missing canary event for autopromotion: %+v", recordedCopy)
	}
	if !promotedObserved {
		t.Fatalf("missing promoted event for autopromotion: %+v", recordedCopy)
	}
}

func TestServiceUpdateCanaryFailureAborts(t *testing.T) {
	apiInitial0 := &fakeInstance{waitCh: make(chan error, 1)}
	apiInitial1 := &fakeInstance{waitCh: make(chan error, 1)}
	apiFailed := &fakeInstance{waitErr: errors.New("canary blew up")}
	apiRollback := &fakeInstance{waitCh: make(chan error, 1)}

	rt := newRecordingRuntime(map[string][]*fakeInstance{
		"api": {apiInitial0, apiInitial1, apiFailed, apiRollback},
	})

	doc := &stack.StackFile{
		Stack: stack.StackMeta{Name: "demo"},
		Services: map[string]*stack.Service{
			"api": {
				Runtime:  "test",
				Replicas: 2,
			},
		},
	}

	graph, err := BuildGraph(doc)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}

	orch := NewOrchestrator(runtimelib.Registry{"test": rt})

	events := make(chan Event, 128)
	var (
		mu       sync.Mutex
		recorded []Event
	)
	drain := make(chan struct{})
	go func() {
		defer close(drain)
		for evt := range events {
			mu.Lock()
			recorded = append(recorded, evt)
			mu.Unlock()
		}
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

	if name := waitForServiceStart(t, rt.startCh); name != "api[0]" {
		t.Fatalf("expected first replica start, got %s", name)
	}
	if name := waitForServiceStart(t, rt.startCh); name != "api[1]" {
		t.Fatalf("expected second replica start, got %s", name)
	}

	apiInitial0.waitCh <- nil
	apiInitial1.waitCh <- nil

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

	service, ok := deployment.Service("api")
	if !ok {
		t.Fatalf("expected api service handle")
	}

	updateDone := make(chan error, 1)
	newSpec := &stack.Service{Runtime: "test", Replicas: 2, Command: []string{"sleep", "1"}}
	go func() {
		updateDone <- service.Update(context.Background(), newSpec)
	}()

	if name := waitForServiceStart(t, rt.startCh); name != "api[2]" {
		t.Fatalf("expected canary restart, got %s", name)
	}

	if name := waitForServiceStart(t, rt.startCh); name != "api[3]" {
		t.Fatalf("expected rollback restart, got %s", name)
	}

	apiRollback.waitCh <- nil

	select {
	case err := <-updateDone:
		if err == nil {
			t.Fatalf("expected update failure")
		} else if !strings.Contains(err.Error(), "promotion failed") {
			t.Fatalf("unexpected update error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for update failure")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := deployment.Stop(stopCtx, events); err != nil {
		t.Fatalf("deployment stop: %v", err)
	}

	close(events)
	<-drain

	mu.Lock()
	recordedCopy := append([]Event(nil), recorded...)
	mu.Unlock()

	abortedObserved := false
	for _, evt := range recordedCopy {
		if evt.Service != "api" {
			continue
		}
		if evt.Type == EventTypeAborted {
			abortedObserved = true
			if evt.Reason != ReasonAborted {
				t.Fatalf("unexpected abort reason: %+v", evt)
			}
			if evt.Err == nil || !strings.Contains(evt.Err.Error(), "canary blew up") {
				t.Fatalf("abort event missing error context: %+v", evt)
			}
		}
		if evt.Type == EventTypePromoted {
			t.Fatalf("unexpected promoted event on failure: %+v", evt)
		}
	}

	if !abortedObserved {
		t.Fatalf("missing aborted event: %+v", recordedCopy)
	}
}

func TestServiceUpdateRollingAbortsAfterReadinessFailures(t *testing.T) {
	apiInitial := &fakeInstance{waitCh: make(chan error, 1)}
	canaryAttempt1 := &fakeInstance{waitCh: make(chan error, 1), healthCh: make(chan probe.State, 4)}
	canaryAttempt2 := &fakeInstance{waitCh: make(chan error, 1), healthCh: make(chan probe.State, 4)}
	rollback := &fakeInstance{waitCh: make(chan error, 1)}
	rollbackExtra := &fakeInstance{waitCh: make(chan error, 1)}

	rt := newRecordingRuntime(map[string][]*fakeInstance{
		"api": {apiInitial, canaryAttempt1, canaryAttempt2, rollback, rollbackExtra},
	})

	doc := &stack.StackFile{
		Stack: stack.StackMeta{Name: "demo"},
		Services: map[string]*stack.Service{
			"api": {
				Runtime:  "test",
				Replicas: 1,
				Command:  []string{"old"},
			},
		},
	}

	graph, err := BuildGraph(doc)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}

	orch := NewOrchestrator(runtimelib.Registry{"test": rt})

	events := make(chan Event, 256)
	var (
		mu       sync.Mutex
		recorded []Event
	)
	drain := make(chan struct{})
	go func() {
		defer close(drain)
		for evt := range events {
			mu.Lock()
			recorded = append(recorded, evt)
			mu.Unlock()
		}
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

	if name := waitForServiceStart(t, rt.startCh); name != "api[0]" {
		t.Fatalf("expected initial start, got %s", name)
	}

	apiInitial.waitCh <- nil

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("orchestrator up failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for orchestrator up")
	}

	if deployment == nil {
		t.Fatalf("expected deployment handle")
	}

	service, ok := deployment.Service("api")
	if !ok {
		t.Fatalf("expected api service handle")
	}

	updateDone := make(chan error, 1)
	newSpec := &stack.Service{
		Runtime:  "test",
		Replicas: 1,
		Command:  []string{"new"},
		Update: &stack.UpdatePolicy{
			AbortAfterFailures: 2,
			ObservationWindow:  stack.Duration{Duration: 10 * time.Second},
		},
	}

	go func() {
		updateDone <- service.Update(context.Background(), newSpec)
	}()

	if name := waitForServiceStart(t, rt.startCh); name != "api[1]" {
		t.Fatalf("expected canary restart, got %s", name)
	}

	canaryAttempt1.waitCh <- nil
	canaryAttempt1.healthCh <- probe.State{Status: probe.StatusReady}
	failure1 := errors.New("probe failed after update")
	canaryAttempt1.healthCh <- probe.State{Status: probe.StatusUnready, Err: failure1}

	if name := waitForServiceStart(t, rt.startCh); name != "api[2]" {
		t.Fatalf("expected second canary restart, got %s", name)
	}

	canaryAttempt2.waitCh <- nil
	canaryAttempt2.healthCh <- probe.State{Status: probe.StatusReady}
	failure2 := errors.New("probe failed again")
	canaryAttempt2.healthCh <- probe.State{Status: probe.StatusUnready, Err: failure2}

	instances := []*fakeInstance{apiInitial, canaryAttempt1, canaryAttempt2, rollback, rollbackExtra}
	parseIndex := func(name string) int {
		start := strings.Index(name, "[")
		end := strings.Index(name, "]")
		if start < 0 || end < 0 || end <= start+1 {
			return -1
		}
		idx, err := strconv.Atoi(name[start+1 : end])
		if err != nil {
			return -1
		}
		return idx
	}

	startErr := make(chan error, 1)
	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		for {
			select {
			case name := <-rt.startCh:
				t.Logf("observed restart %s", name)
				idx := parseIndex(name)
				if idx < 0 || idx >= len(instances) {
					startErr <- fmt.Errorf("unexpected restart %s", name)
					return
				}
				inst := instances[idx]
				if inst == rollback {
					inst.waitCh <- nil
					return
				}
				inst.waitCh <- context.Canceled
			case <-time.After(5 * time.Second):
				startErr <- fmt.Errorf("timed out waiting for rollback start")
				return
			}
		}
	}()

	var updateErr error
	select {
	case updateErr = <-updateDone:
	case <-time.After(5 * time.Second):
		mu.Lock()
		snapshot := append([]Event(nil), recorded...)
		mu.Unlock()
		t.Fatalf("timed out waiting for update abort, recorded=%+v", snapshot)
	}

	<-startDone
	select {
	case err := <-startErr:
		if err != nil {
			t.Fatalf("start handler error: %v", err)
		}
	default:
	}

	if updateErr == nil {
		t.Fatalf("expected update failure")
	}
	if !strings.Contains(updateErr.Error(), "promotion aborted after 2 readiness failures") {
		t.Fatalf("unexpected update error: %v", updateErr)
	}

	if spec := service.handle.service; spec == nil || len(spec.Command) == 0 || spec.Command[0] != "old" {
		t.Fatalf("expected service spec rollback, got %+v", spec)
	}
	if supervisorSpec := service.handle.replicas[0].supervisor.serviceSpec(); supervisorSpec == nil || len(supervisorSpec.Command) == 0 || supervisorSpec.Command[0] != "old" {
		t.Fatalf("expected supervisor spec rollback, got %+v", supervisorSpec)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := deployment.Stop(stopCtx, events); err != nil {
		t.Fatalf("deployment stop: %v", err)
	}

	close(events)
	<-drain

	mu.Lock()
	recordedCopy := append([]Event(nil), recorded...)
	mu.Unlock()

	abortedObserved := false
	for _, evt := range recordedCopy {
		if evt.Service != "api" || evt.Type != EventTypeAborted {
			continue
		}
		abortedObserved = true
		if !strings.Contains(evt.Message, "readiness failures") {
			t.Fatalf("aborted event missing details: %+v", evt)
		}
		if evt.Err == nil || !strings.Contains(evt.Err.Error(), "last failure") {
			t.Fatalf("aborted event missing context: %+v", evt)
		}
	}

	if !abortedObserved {
		t.Fatalf("expected aborted event, got %+v", recordedCopy)
	}
}

func TestServiceUpdateRollingFailureWindowResetsCount(t *testing.T) {
	apiInitial := &fakeInstance{waitCh: make(chan error, 1)}
	attempt1 := &fakeInstance{waitCh: make(chan error, 1), healthCh: make(chan probe.State, 4)}
	attempt2 := &fakeInstance{waitCh: make(chan error, 1), healthCh: make(chan probe.State, 4)}
	attempt3 := &fakeInstance{waitCh: make(chan error, 1), healthCh: make(chan probe.State, 4)}
	attempt4 := &fakeInstance{waitCh: make(chan error, 1)}

	rt := newRecordingRuntime(map[string][]*fakeInstance{
		"api": {apiInitial, attempt1, attempt2, attempt3, attempt4},
	})

	doc := &stack.StackFile{
		Stack: stack.StackMeta{Name: "demo"},
		Services: map[string]*stack.Service{
			"api": {
				Runtime:  "test",
				Replicas: 1,
				Command:  []string{"old"},
			},
		},
	}

	graph, err := BuildGraph(doc)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}

	orch := NewOrchestrator(runtimelib.Registry{"test": rt})

	events := make(chan Event, 256)
	var (
		mu       sync.Mutex
		recorded []Event
	)
	drain := make(chan struct{})
	go func() {
		defer close(drain)
		for evt := range events {
			mu.Lock()
			recorded = append(recorded, evt)
			mu.Unlock()
		}
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

	if name := waitForServiceStart(t, rt.startCh); name != "api[0]" {
		t.Fatalf("expected initial start, got %s", name)
	}

	apiInitial.waitCh <- nil

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("orchestrator up failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for orchestrator up")
	}

	if deployment == nil {
		t.Fatalf("expected deployment handle")
	}

	service, ok := deployment.Service("api")
	if !ok {
		t.Fatalf("expected api service handle")
	}

	updateDone := make(chan error, 1)
	window := 50 * time.Millisecond
	newSpec := &stack.Service{
		Runtime:  "test",
		Replicas: 1,
		Command:  []string{"new"},
		Update: &stack.UpdatePolicy{
			AbortAfterFailures: 2,
			ObservationWindow:  stack.Duration{Duration: window},
		},
	}

	go func() {
		updateDone <- service.Update(context.Background(), newSpec)
	}()

	waitForStart := func(expected string) {
		select {
		case name := <-rt.startCh:
			if name != expected {
				t.Fatalf("expected %s, got %s", expected, name)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timeout waiting for %s", expected)
		}
	}

	waitForStart("api[1]")

	attempt1.waitCh <- nil
	attempt1.healthCh <- probe.State{Status: probe.StatusReady}
	failure1 := errors.New("initial readiness failure")
	attempt1.healthCh <- probe.State{Status: probe.StatusUnready, Err: failure1}

	waitForStart("api[2]")

	attempt2.waitCh <- nil
	attempt2.healthCh <- probe.State{Status: probe.StatusReady}

	time.Sleep(4 * window)

	failure2 := errors.New("late readiness failure")
	attempt2.healthCh <- probe.State{Status: probe.StatusUnready, Err: failure2}

	waitForStart("api[3]")

	attempt3.waitCh <- nil
	attempt3.healthCh <- probe.State{Status: probe.StatusReady}

	promoteDone := make(chan error, 1)
	go func() {
		promoteDone <- service.Promote(context.Background())
	}()

	var updateErr error
	select {
	case updateErr = <-updateDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for update completion")
	}

	if updateErr != nil {
		t.Fatalf("unexpected update error: %v", updateErr)
	}

	select {
	case promoteErr := <-promoteDone:
		if promoteErr != nil {
			t.Fatalf("promote failed: %v", promoteErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for promote to finish")
	}

	if spec := service.handle.service; spec == nil || len(spec.Command) == 0 || spec.Command[0] != "new" {
		t.Fatalf("expected service spec updated, got %+v", spec)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := deployment.Stop(stopCtx, events); err != nil {
		t.Fatalf("deployment stop: %v", err)
	}

	close(events)
	<-drain

	mu.Lock()
	recordedCopy := append([]Event(nil), recorded...)
	mu.Unlock()

	for _, evt := range recordedCopy {
		if evt.Service == "api" && evt.Type == EventTypeAborted {
			t.Fatalf("unexpected aborted event: %+v", evt)
		}
	}
}

func TestServiceUpdateBlueGreenSuccess(t *testing.T) {
	blue0 := &fakeInstance{waitCh: make(chan error, 1), stopped: make(chan struct{}, 1)}
	blue1 := &fakeInstance{waitCh: make(chan error, 1), stopped: make(chan struct{}, 1)}
	green0 := &fakeInstance{waitCh: make(chan error, 1)}
	green1 := &fakeInstance{waitCh: make(chan error, 1)}

	rt := newRecordingRuntime(map[string][]*fakeInstance{
		"api": {blue0, blue1, green0, green1},
	})

	doc := &stack.StackFile{
		Stack: stack.StackMeta{Name: "demo"},
		Services: map[string]*stack.Service{
			"api": {
				Runtime:  "test",
				Replicas: 2,
			},
		},
	}

	graph, err := BuildGraph(doc)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}

	orch := NewOrchestrator(runtimelib.Registry{"test": rt})

	events := make(chan Event, 256)
	var (
		mu       sync.Mutex
		recorded []Event
	)
	drain := make(chan struct{})
	go func() {
		defer close(drain)
		for evt := range events {
			mu.Lock()
			recorded = append(recorded, evt)
			mu.Unlock()
		}
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

	if name := waitForServiceStart(t, rt.startCh); name != "api[0]" {
		t.Fatalf("expected first replica start, got %s", name)
	}
	if name := waitForServiceStart(t, rt.startCh); name != "api[1]" {
		t.Fatalf("expected second replica start, got %s", name)
	}

	blue0.waitCh <- nil
	blue1.waitCh <- nil

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

	service, ok := deployment.Service("api")
	if !ok {
		t.Fatalf("expected api service handle")
	}

	updateDone := make(chan error, 1)
	newSpec := &stack.Service{
		Runtime:  "test",
		Replicas: 2,
		Command:  []string{"sleep", "1"},
		Update: &stack.UpdatePolicy{
			Strategy: "blueGreen",
			BlueGreen: &stack.BlueGreen{
				DrainTimeout:   stack.Duration{Duration: 10 * time.Millisecond},
				RollbackWindow: stack.Duration{Duration: 25 * time.Millisecond},
				Switch:         "ports",
			},
		},
	}

	go func() {
		updateDone <- service.Update(context.Background(), newSpec)
	}()

	if name := waitForServiceStart(t, rt.startCh); name != "api[2]" {
		t.Fatalf("expected first green replica start, got %s", name)
	}
	if name := waitForServiceStart(t, rt.startCh); name != "api[3]" {
		t.Fatalf("expected second green replica start, got %s", name)
	}

	green0.waitCh <- nil
	green1.waitCh <- nil

	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf("blue-green update failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for blue-green update to complete")
	}

	select {
	case <-blue0.stopped:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("expected blue replica 0 to decommission")
	}
	select {
	case <-blue1.stopped:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("expected blue replica 1 to decommission")
	}

	if service.Replicas() != 2 {
		t.Fatalf("expected 2 replicas after blue-green update, got %d", service.Replicas())
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := deployment.Stop(stopCtx, events); err != nil {
		t.Fatalf("deployment stop: %v", err)
	}

	close(events)
	<-drain

	mu.Lock()
	recordedCopy := append([]Event(nil), recorded...)
	mu.Unlock()

	phases := []string{}
	messages := []string{}
	promoted := false
	cutoverIdx := -1
	promotedIdx := -1
	decommissionIdx := -1
	stopIndices := []int{}
	for idx, evt := range recordedCopy {
		if evt.Service != "api" {
			continue
		}
		switch evt.Type {
		case EventTypeUpdatePhase:
			phases = append(phases, evt.Reason)
			messages = append(messages, evt.Message)
			switch evt.Reason {
			case ReasonBlueGreenCutover:
				if cutoverIdx == -1 {
					cutoverIdx = idx
				}
			case ReasonBlueGreenDecommission:
				if decommissionIdx == -1 {
					decommissionIdx = idx
				}
			}
		case EventTypePromoted:
			promoted = true
			if promotedIdx == -1 {
				promotedIdx = idx
			}
		case EventTypeAborted:
			t.Fatalf("unexpected aborted event during successful blue-green update: %+v", evt)
		case EventTypeStopping:
			stopIndices = append(stopIndices, idx)
		}
	}

	expectedPhases := []string{
		ReasonBlueGreenProvision,
		ReasonBlueGreenVerify,
		ReasonBlueGreenCutover,
		ReasonBlueGreenDecommission,
	}
	if len(phases) < len(expectedPhases) {
		t.Fatalf("missing blue-green phase events: got %v", phases)
	}
	for i, reason := range expectedPhases {
		if phases[i] != reason {
			t.Fatalf("expected phase %s at position %d, got %s", reason, i, phases[i])
		}
	}
	expectedMessages := []string{
		blueGreenPhaseMessage(BlueGreenPhaseProvisionGreen),
		blueGreenPhaseMessage(BlueGreenPhaseVerify),
		fmt.Sprintf("%s (switch=%s)", blueGreenPhaseMessage(BlueGreenPhaseCutover), stack.BlueGreenSwitchPorts),
		blueGreenPhaseMessage(BlueGreenPhaseDecommission),
	}
	if len(messages) < len(expectedMessages) {
		t.Fatalf("missing blue-green phase messages: got %v", messages)
	}
	for i, msg := range expectedMessages {
		if messages[i] != msg {
			t.Fatalf("expected phase message %q at position %d, got %q", msg, i, messages[i])
		}
	}
	if !promoted {
		t.Fatalf("missing promoted event for blue-green update: %+v", recordedCopy)
	}

	if cutoverIdx == -1 {
		t.Fatalf("missing blue-green cutover event in %+v", recordedCopy)
	}
	if promotedIdx == -1 {
		t.Fatalf("missing promoted event index in %+v", recordedCopy)
	}
	if decommissionIdx == -1 {
		t.Fatalf("missing blue-green decommission event in %+v", recordedCopy)
	}
	if cutoverIdx > promotedIdx {
		t.Fatalf("cutover event should precede promotion, events: %+v", recordedCopy)
	}
	if promotedIdx > decommissionIdx {
		t.Fatalf("promotion should occur before decommission, events: %+v", recordedCopy)
	}
	if len(stopIndices) < 2 {
		t.Fatalf("expected at least two stop events for blue replicas, got %d (%+v)", len(stopIndices), recordedCopy)
	}
	for i := 0; i < 2; i++ {
		idx := stopIndices[i]
		if idx <= decommissionIdx {
			t.Fatalf("blue replica stopped before decommission phase completed, events: %+v", recordedCopy)
		}
	}
}

func TestServiceUpdateBlueGreenProxyLabelSwitchNormalization(t *testing.T) {
	blue := &fakeInstance{waitCh: make(chan error, 1)}
	green := &fakeInstance{waitCh: make(chan error, 1)}

	rt := newRecordingRuntime(map[string][]*fakeInstance{
		"api": {blue, green},
	})

	doc := &stack.StackFile{
		Stack: stack.StackMeta{Name: "demo"},
		Services: map[string]*stack.Service{
			"api": {
				Runtime:  "test",
				Replicas: 1,
			},
		},
	}

	graph, err := BuildGraph(doc)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}

	orch := NewOrchestrator(runtimelib.Registry{"test": rt})

	events := make(chan Event, 64)
	var (
		mu       sync.Mutex
		recorded []Event
	)
	drain := make(chan struct{})
	go func() {
		defer close(drain)
		for evt := range events {
			mu.Lock()
			recorded = append(recorded, evt)
			mu.Unlock()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	upErrCh := make(chan error, 1)
	var deployment *Deployment
	go func() {
		var err error
		deployment, err = orch.Up(ctx, doc, graph, events)
		upErrCh <- err
	}()

	if name := waitForServiceStart(t, rt.startCh); name != "api[0]" {
		t.Fatalf("expected initial replica start, got %s", name)
	}
	blue.waitCh <- nil

	select {
	case err := <-upErrCh:
		if err != nil {
			t.Fatalf("orchestrator up failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for orchestrator up")
	}

	service, ok := deployment.Service("api")
	if !ok {
		t.Fatalf("expected api service handle")
	}

	updateDone := make(chan error, 1)
	newSpec := &stack.Service{
		Runtime:  "test",
		Replicas: 1,
		Update: &stack.UpdatePolicy{
			Strategy: "blueGreen",
			BlueGreen: &stack.BlueGreen{
				Switch: "Proxy-Label",
			},
		},
	}

	go func() {
		updateDone <- service.Update(context.Background(), newSpec)
	}()

	if name := waitForServiceStart(t, rt.startCh); name != "api[1]" {
		t.Fatalf("expected green replica start, got %s", name)
	}
	green.waitCh <- nil

	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf("blue-green update failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for blue-green update")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := deployment.Stop(stopCtx, events); err != nil {
		t.Fatalf("deployment stop: %v", err)
	}

	close(events)
	<-drain

	mu.Lock()
	recordedCopy := append([]Event(nil), recorded...)
	mu.Unlock()

	found := false
	for _, evt := range recordedCopy {
		if evt.Service != "api" || evt.Type != EventTypeUpdatePhase || evt.Reason != ReasonBlueGreenCutover {
			continue
		}
		if evt.Message == fmt.Sprintf("%s (switch=%s)", blueGreenPhaseMessage(BlueGreenPhaseCutover), stack.BlueGreenSwitchProxyLabel) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected cutover event with proxyLabel switch, got %+v", recordedCopy)
	}
}

func TestServiceUpdateBlueGreenInvalidSwitch(t *testing.T) {
	blue := &fakeInstance{waitCh: make(chan error, 1)}
	green := &fakeInstance{waitCh: make(chan error, 1)}

	rt := newRecordingRuntime(map[string][]*fakeInstance{
		"api": {blue, green},
	})

	doc := &stack.StackFile{
		Stack: stack.StackMeta{Name: "demo"},
		Services: map[string]*stack.Service{
			"api": {
				Runtime:  "test",
				Replicas: 1,
			},
		},
	}

	graph, err := BuildGraph(doc)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}

	orch := NewOrchestrator(runtimelib.Registry{"test": rt})

	events := make(chan Event, 64)
	drain := make(chan struct{})
	go func() {
		defer close(drain)
		for range events {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	upErrCh := make(chan error, 1)
	var deployment *Deployment
	go func() {
		var err error
		deployment, err = orch.Up(ctx, doc, graph, events)
		upErrCh <- err
	}()

	if name := waitForServiceStart(t, rt.startCh); name != "api[0]" {
		t.Fatalf("expected initial replica start, got %s", name)
	}
	blue.waitCh <- nil

	select {
	case err := <-upErrCh:
		if err != nil {
			t.Fatalf("orchestrator up failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for orchestrator up")
	}

	service, ok := deployment.Service("api")
	if !ok {
		t.Fatalf("expected api service handle")
	}

	newSpec := &stack.Service{
		Runtime:  "test",
		Replicas: 1,
		Update: &stack.UpdatePolicy{
			Strategy: "blueGreen",
			BlueGreen: &stack.BlueGreen{
				Switch: "invalid",
			},
		},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- service.Update(context.Background(), newSpec)
	}()

	if name := waitForServiceStart(t, rt.startCh); name != "api[1]" {
		t.Fatalf("expected green replica start, got %s", name)
	}
	green.waitCh <- nil

	var updateErr error
	select {
	case updateErr = <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for update error")
	}

	if updateErr == nil {
		t.Fatalf("expected update failure for invalid switch")
	}
	if !strings.Contains(updateErr.Error(), "unsupported value") {
		t.Fatalf("unexpected error: %v", updateErr)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := deployment.Stop(stopCtx, events); err != nil {
		t.Fatalf("deployment stop: %v", err)
	}

	close(events)
	<-drain
}

func TestServiceUpdateBlueGreenVerifyFailureRollsBack(t *testing.T) {
	blue0 := &fakeInstance{waitCh: make(chan error, 1), stopped: make(chan struct{}, 1)}
	blue1 := &fakeInstance{waitCh: make(chan error, 1), stopped: make(chan struct{}, 1)}
	greenFailed := &fakeInstance{waitCh: make(chan error, 1), stopped: make(chan struct{}, 1)}
	greenSecond := &fakeInstance{waitCh: make(chan error, 1)}

	rt := newRecordingRuntime(map[string][]*fakeInstance{
		"api": {blue0, blue1, greenFailed, greenSecond},
	})

	doc := &stack.StackFile{
		Stack: stack.StackMeta{Name: "demo"},
		Services: map[string]*stack.Service{
			"api": {
				Runtime:  "test",
				Replicas: 2,
			},
		},
	}

	graph, err := BuildGraph(doc)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}

	orch := NewOrchestrator(runtimelib.Registry{"test": rt})

	events := make(chan Event, 256)
	var (
		mu       sync.Mutex
		recorded []Event
	)
	drain := make(chan struct{})
	go func() {
		defer close(drain)
		for evt := range events {
			mu.Lock()
			recorded = append(recorded, evt)
			mu.Unlock()
		}
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

	if name := waitForServiceStart(t, rt.startCh); name != "api[0]" {
		t.Fatalf("expected first replica start, got %s", name)
	}
	if name := waitForServiceStart(t, rt.startCh); name != "api[1]" {
		t.Fatalf("expected second replica start, got %s", name)
	}

	blue0.waitCh <- nil
	blue1.waitCh <- nil

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

	service, ok := deployment.Service("api")
	if !ok {
		t.Fatalf("expected api service handle")
	}

	updateDone := make(chan error, 1)
	newSpec := &stack.Service{
		Runtime:  "test",
		Replicas: 2,
		Command:  []string{"sleep", "1"},
		Update: &stack.UpdatePolicy{
			Strategy: "blueGreen",
		},
	}

	go func() {
		updateDone <- service.Update(context.Background(), newSpec)
	}()

	if name := waitForServiceStart(t, rt.startCh); name != "api[2]" {
		t.Fatalf("expected first green replica start, got %s", name)
	}
	if name := waitForServiceStart(t, rt.startCh); name != "api[3]" {
		t.Fatalf("expected second green replica start, got %s", name)
	}

	greenFailed.waitCh <- errors.New("green readiness failed")
	greenSecond.waitCh <- nil

	select {
	case err := <-updateDone:
		if err == nil {
			t.Fatalf("expected blue-green update failure")
		}
		if !strings.Contains(err.Error(), "blue-green verification failed") {
			t.Fatalf("unexpected blue-green update error: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for blue-green update failure")
	}

	select {
	case <-greenFailed.stopped:
	case <-time.After(10 * time.Second):
		t.Fatalf("expected failed green replica to be stopped")
	}

	blue0Stopped := false
	blue1Stopped := false
	select {
	case <-blue0.stopped:
		blue0Stopped = true
	default:
	}
	select {
	case <-blue1.stopped:
		blue1Stopped = true
	default:
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := deployment.Stop(stopCtx, events); err != nil {
		t.Fatalf("deployment stop: %v", err)
	}

	close(events)
	<-drain

	mu.Lock()
	recordedCopy := append([]Event(nil), recorded...)
	mu.Unlock()

	aborted := false
	for _, evt := range recordedCopy {
		if evt.Service != "api" {
			continue
		}
		switch evt.Type {
		case EventTypeAborted:
			aborted = true
			if evt.Reason != ReasonBlueGreenRollback {
				t.Fatalf("unexpected abort reason: %+v", evt)
			}
		case EventTypePromoted:
			t.Fatalf("unexpected promoted event during blue-green rollback: %+v", evt)
		}
	}

	if !aborted {
		t.Fatalf("expected blue-green rollback event: %+v", recordedCopy)
	}
	if blue0Stopped || blue1Stopped {
		t.Fatalf("blue replicas stopped during rollback: %+v", recordedCopy)
	}
}

func TestServiceUpdateBlueGreenRollbackOnGreenFailureDuringWindow(t *testing.T) {
	blue0 := &fakeInstance{waitCh: make(chan error, 1), stopped: make(chan struct{}, 1)}
	blue1 := &fakeInstance{waitCh: make(chan error, 1), stopped: make(chan struct{}, 1)}
	green0 := &fakeInstance{waitCh: make(chan error, 1), healthCh: make(chan probe.State, 1), stopped: make(chan struct{}, 1)}
	green1 := &fakeInstance{waitCh: make(chan error, 1), healthCh: make(chan probe.State, 1), stopped: make(chan struct{}, 1)}

	rt := newRecordingRuntime(map[string][]*fakeInstance{
		"api": {blue0, blue1, green0, green1},
	})

	doc := &stack.StackFile{
		Stack: stack.StackMeta{Name: "demo"},
		Services: map[string]*stack.Service{
			"api": {
				Runtime:  "test",
				Replicas: 2,
			},
		},
	}

	graph, err := BuildGraph(doc)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}

	orch := NewOrchestrator(runtimelib.Registry{"test": rt})

	events := make(chan Event, 256)
	var (
		mu       sync.Mutex
		recorded []Event
	)
	drain := make(chan struct{})
	go func() {
		defer close(drain)
		for evt := range events {
			mu.Lock()
			recorded = append(recorded, evt)
			mu.Unlock()
		}
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

	if name := waitForServiceStart(t, rt.startCh); name != "api[0]" {
		t.Fatalf("expected first replica start, got %s", name)
	}
	if name := waitForServiceStart(t, rt.startCh); name != "api[1]" {
		t.Fatalf("expected second replica start, got %s", name)
	}

	blue0.waitCh <- nil
	blue1.waitCh <- nil

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

	service, ok := deployment.Service("api")
	if !ok {
		t.Fatalf("expected api service handle")
	}

	updateDone := make(chan error, 1)
	newSpec := &stack.Service{
		Runtime:  "test",
		Replicas: 2,
		Command:  []string{"sleep", "1"},
		Update: &stack.UpdatePolicy{
			Strategy: "blueGreen",
			BlueGreen: &stack.BlueGreen{
				DrainTimeout:   stack.Duration{Duration: 10 * time.Millisecond},
				RollbackWindow: stack.Duration{Duration: 200 * time.Millisecond},
				Switch:         "ports",
			},
		},
	}

	go func() {
		updateDone <- service.Update(context.Background(), newSpec)
	}()

	if name := waitForServiceStart(t, rt.startCh); name != "api[2]" {
		t.Fatalf("expected first green replica start, got %s", name)
	}
	if name := waitForServiceStart(t, rt.startCh); name != "api[3]" {
		t.Fatalf("expected second green replica start, got %s", name)
	}

	green0.waitCh <- nil
	green1.waitCh <- nil

	waitForEvent := func(match func(Event) bool) {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			for _, evt := range recorded {
				if match(evt) {
					mu.Unlock()
					return
				}
			}
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for event")
	}

	waitForEvent(func(evt Event) bool {
		return evt.Service == "api" && evt.Type == EventTypePromoted && evt.Reason == ReasonPromoted
	})

	failureErr := errors.New("green replica unhealthy")
	green0.healthCh <- probe.State{Status: probe.StatusUnready, Err: failureErr}

	var updateErr error
	select {
	case updateErr = <-updateDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for blue-green rollback")
	}

	if updateErr == nil {
		t.Fatalf("expected blue-green rollback error")
	}
	if !strings.Contains(updateErr.Error(), "blue-green rollback triggered") {
		t.Fatalf("unexpected rollback error: %v", updateErr)
	}

	if service.Replicas() != 2 {
		t.Fatalf("expected blue replicas to remain active, got %d", service.Replicas())
	}

	select {
	case <-green0.stopped:
	case <-time.After(time.Second):
		t.Fatalf("expected green replica 0 to shut down during rollback")
	}
	select {
	case <-green1.stopped:
	case <-time.After(time.Second):
		t.Fatalf("expected green replica 1 to shut down during rollback")
	}

	select {
	case <-blue0.stopped:
		t.Fatalf("blue replica 0 stopped unexpectedly during rollback")
	default:
	}
	select {
	case <-blue1.stopped:
		t.Fatalf("blue replica 1 stopped unexpectedly during rollback")
	default:
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := deployment.Stop(stopCtx, events); err != nil {
		t.Fatalf("deployment stop: %v", err)
	}

	close(events)
	<-drain

	mu.Lock()
	recordedCopy := append([]Event(nil), recorded...)
	mu.Unlock()

	aborted := false
	for _, evt := range recordedCopy {
		if evt.Service != "api" {
			continue
		}
		if evt.Type == EventTypeAborted {
			aborted = true
			if evt.Reason != ReasonBlueGreenRollback {
				t.Fatalf("unexpected rollback reason: %+v", evt)
			}
			if evt.Err == nil || !strings.Contains(evt.Err.Error(), "blue-green rollback triggered") {
				t.Fatalf("rollback event missing context: %+v", evt)
			}
		}
	}

	if !aborted {
		t.Fatalf("expected blue-green rollback event, got %+v", recordedCopy)
	}
}

type recordingRuntime struct {
	mu        sync.Mutex
	instances map[string][]*fakeInstance
	startCh   chan string
	starts    map[string]int
}

func newRecordingRuntime(instances map[string][]*fakeInstance) *recordingRuntime {
	return &recordingRuntime{
		instances: instances,
		startCh:   make(chan string, 32),
		starts:    make(map[string]int),
	}
}

func (r *recordingRuntime) Start(ctx context.Context, spec runtimelib.StartSpec) (runtimelib.Handle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := spec.Name
	insts, ok := r.instances[name]
	if !ok {
		return nil, fmt.Errorf("no instance configured for service %s", name)
	}
	idx := r.starts[name]
	if idx >= len(insts) {
		return nil, fmt.Errorf("no more instances configured for service %s", name)
	}
	r.starts[name] = idx + 1
	if r.startCh != nil {
		r.startCh <- fmt.Sprintf("%s[%d]", name, idx)
	}
	return insts[idx], nil
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
