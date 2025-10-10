package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/probe"
	"github.com/Paintersrp/orco/internal/runtime"
	"github.com/Paintersrp/orco/internal/stack"
)

func TestSupervisorRestartsOnUnready(t *testing.T) {
	svc := &stack.Service{
		RestartPolicy: &stack.RestartPolicy{
			MaxRetries: 3,
			Backoff: &stack.Backoff{
				Min:    stack.Duration{Duration: 10 * time.Millisecond},
				Max:    stack.Duration{Duration: 100 * time.Millisecond},
				Factor: 2,
			},
		},
	}

	first := &fakeInstance{
		waitCh:   make(chan error, 1),
		healthCh: make(chan probe.State, 4),
	}
	second := &fakeInstance{
		waitCh:   make(chan error, 1),
		healthCh: make(chan probe.State, 2),
	}

	rt := &fakeRuntime{
		instances: []*fakeInstance{first, second},
		startCh:   make(chan struct{}, 4),
	}

	events := make(chan Event, 32)
	sup := newSupervisor("web", 0, svc, rt, events, nil)
	sup.jitter = func(d time.Duration) time.Duration { return d }
	sup.sleep = func(ctx context.Context, d time.Duration) error { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sup.Start(ctx)

	// Wait for initial start to be invoked.
	waitForStart(t, rt.startCh)

	// Drive the first instance to ready.
	first.waitCh <- nil
	first.healthCh <- probe.State{Status: probe.StatusReady}

	if err := sup.AwaitReady(context.Background()); err != nil {
		t.Fatalf("await ready: %v", err)
	}

	// Trigger an unready transition that should initiate a restart.
	failure := errors.New("probe failed")
	first.healthCh <- probe.State{Status: probe.StatusUnready, Err: failure}

	// Allow the supervisor to acquire the second instance.
	waitForStart(t, rt.startCh)

	second.waitCh <- nil
	second.healthCh <- probe.State{Status: probe.StatusReady}

	// Collect events until the second instance reports readiness.
	var observed []Event
	deadline := time.After(time.Second)
eventsLoop:
	for {
		select {
		case evt := <-events:
			if evt.Service != "web" {
				continue
			}
			observed = append(observed, evt)
			if evt.Type == EventTypeReady && evt.Attempt == 2 {
				break eventsLoop
			}
		case <-deadline:
			t.Fatalf("timed out waiting for events; got %v", observed)
		}
	}

	types := make([]EventType, 0, len(observed))
	for _, evt := range observed {
		types = append(types, evt.Type)
	}

	// Expect at least one unready, crash and a subsequent starting event.
	if !containsSequence(types, []EventType{EventTypeUnready, EventTypeCrashed, EventTypeStarting, EventTypeReady}) {
		t.Fatalf("expected restart sequence, got %v", types)
	}

	var startingEvents []Event
	var readyEvents []Event
	var unreadyEvents []Event
	var crashedEvents []Event
	for _, evt := range observed {
		switch evt.Type {
		case EventTypeStarting:
			startingEvents = append(startingEvents, evt)
		case EventTypeReady:
			readyEvents = append(readyEvents, evt)
		case EventTypeUnready:
			unreadyEvents = append(unreadyEvents, evt)
		case EventTypeCrashed:
			crashedEvents = append(crashedEvents, evt)
		}
	}

	if len(startingEvents) < 2 {
		t.Fatalf("expected two starting events, got %d", len(startingEvents))
	}
	if startingEvents[0].Attempt != 1 || startingEvents[0].Reason != ReasonInitialStart {
		t.Fatalf("first start attempt metadata mismatch: %+v", startingEvents[0])
	}
	if startingEvents[1].Attempt != 2 || startingEvents[1].Reason != ReasonRestart {
		t.Fatalf("restart attempt metadata mismatch: %+v", startingEvents[1])
	}

	if len(readyEvents) < 2 {
		t.Fatalf("expected two ready events, got %d", len(readyEvents))
	}
	if readyEvents[0].Attempt != 1 || readyEvents[0].Reason != ReasonProbeReady {
		t.Fatalf("first ready metadata mismatch: %+v", readyEvents[0])
	}
	if readyEvents[1].Attempt != 2 || readyEvents[1].Reason != ReasonProbeReady {
		t.Fatalf("second ready metadata mismatch: %+v", readyEvents[1])
	}

	if len(unreadyEvents) == 0 {
		t.Fatalf("expected unready event")
	}
	if unreadyEvents[0].Attempt != 1 || unreadyEvents[0].Reason != ReasonProbeUnready {
		t.Fatalf("unready metadata mismatch: %+v", unreadyEvents[0])
	}

	if len(crashedEvents) == 0 {
		t.Fatalf("expected crash event")
	}
	if crashedEvents[0].Attempt != 1 || crashedEvents[0].Reason != ReasonInstanceCrash {
		t.Fatalf("crash metadata mismatch: %+v", crashedEvents[0])
	}

	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("stop supervisor: %v", err)
	}
}

func TestSupervisorBackoffJitter(t *testing.T) {
	svc := &stack.Service{
		RestartPolicy: &stack.RestartPolicy{
			MaxRetries: 3,
			Backoff: &stack.Backoff{
				Min:    stack.Duration{Duration: 50 * time.Millisecond},
				Max:    stack.Duration{Duration: 500 * time.Millisecond},
				Factor: 2,
			},
		},
	}

	fail := &fakeInstance{waitErr: errors.New("not ready")}
	fail2 := &fakeInstance{waitErr: errors.New("still failing")}
	fail3 := &fakeInstance{waitErr: errors.New("boom")}
	fail4 := &fakeInstance{waitErr: errors.New("boom again")}

	rt := &fakeRuntime{instances: []*fakeInstance{fail, fail2, fail3, fail4}}

	delayCh := make(chan time.Duration, 8)
	var delays []time.Duration
	sup := newSupervisor("db", 0, svc, rt, make(chan Event, 32), nil)
	sup.jitter = func(d time.Duration) time.Duration { return d }
	sup.sleep = func(ctx context.Context, d time.Duration) error {
		delayCh <- d
		return nil
	}

	sup.Start(context.Background())

	if err := sup.AwaitReady(context.Background()); err == nil {
		t.Fatalf("expected readiness failure")
	}

	expected := []time.Duration{
		50 * time.Millisecond,
		100 * time.Millisecond,
		200 * time.Millisecond,
	}

	for len(delays) < len(expected) {
		select {
		case d := <-delayCh:
			delays = append(delays, d)
		case <-time.After(time.Second):
			t.Fatalf("expected %d backoff delays, got %d (%v)", len(expected), len(delays), delays)
		}
	}

	sup.Stop(context.Background())

	if len(delays) != len(expected) {
		t.Fatalf("expected %d backoff delays, got %d (%v)", len(expected), len(delays), delays)
	}

	for i, d := range expected {
		if delays[i] != d {
			t.Fatalf("delay %d: expected %v, got %v", i, d, delays[i])
		}
	}
}

func TestSupervisorMaxRetriesEmitsFailed(t *testing.T) {
	svc := &stack.Service{
		RestartPolicy: &stack.RestartPolicy{
			MaxRetries: 1,
			Backoff: &stack.Backoff{
				Min:    stack.Duration{Duration: 10 * time.Millisecond},
				Max:    stack.Duration{Duration: 20 * time.Millisecond},
				Factor: 2,
			},
		},
	}

	inst1 := &fakeInstance{waitErr: errors.New("startup failure")}
	inst2 := &fakeInstance{waitErr: errors.New("still broken")}

	events := make(chan Event, 32)
	rt := &fakeRuntime{instances: []*fakeInstance{inst1, inst2}}

	sup := newSupervisor("api", 0, svc, rt, events, nil)
	sup.jitter = func(d time.Duration) time.Duration { return d }
	sup.sleep = func(ctx context.Context, d time.Duration) error { return nil }

	sup.Start(context.Background())

	if err := sup.AwaitReady(context.Background()); err == nil {
		t.Fatalf("expected readiness failure")
	}

	sup.Stop(context.Background())

	var crashedEvents []Event
	var failedEvents []Event
	var failedErr error
	for len(events) > 0 {
		evt := <-events
		if evt.Service != "api" {
			continue
		}
		switch evt.Type {
		case EventTypeCrashed:
			crashedEvents = append(crashedEvents, evt)
		case EventTypeFailed:
			failedEvents = append(failedEvents, evt)
			failedErr = evt.Err
		}
	}

	if len(crashedEvents) != 2 {
		t.Fatalf("expected two crashed events, got %d", len(crashedEvents))
	}
	if crashedEvents[0].Attempt != 1 || crashedEvents[0].Reason != ReasonInstanceCrash {
		t.Fatalf("first crash metadata mismatch: %+v", crashedEvents[0])
	}
	if crashedEvents[1].Attempt != 2 || crashedEvents[1].Reason != ReasonInstanceCrash {
		t.Fatalf("second crash metadata mismatch: %+v", crashedEvents[1])
	}

	if len(failedEvents) != 1 {
		t.Fatalf("expected failed event after exhausting retries")
	}
	if failedEvents[0].Attempt != 2 || failedEvents[0].Reason != ReasonRetriesExhaust {
		t.Fatalf("failed event metadata mismatch: %+v", failedEvents[0])
	}
	if !errors.Is(failedErr, inst2.waitErr) {
		t.Fatalf("failed event should carry last error; got %v want %v", failedErr, inst2.waitErr)
	}
}

func TestSupervisorPropagatesOOMError(t *testing.T) {
	svc := &stack.Service{
		RestartPolicy: &stack.RestartPolicy{MaxRetries: 0},
	}

	oomErr := errors.New("container terminated by the kernel OOM killer (memory limit 256Mi): container exited with status 137")
	inst := &fakeInstance{waitErr: oomErr}

	events := make(chan Event, 8)
	rt := &fakeRuntime{instances: []*fakeInstance{inst}}

	sup := newSupervisor("api", 0, svc, rt, events, nil)
	sup.jitter = func(d time.Duration) time.Duration { return d }
	sup.sleep = func(ctx context.Context, d time.Duration) error { return nil }

	sup.Start(context.Background())

	if err := sup.AwaitReady(context.Background()); err != oomErr {
		t.Fatalf("expected await ready to return OOM error: got %v want %v", err, oomErr)
	}

	var crash Event
	timeout := time.After(time.Second)
	for {
		select {
		case evt := <-events:
			if evt.Service != "api" {
				continue
			}
			if evt.Type == EventTypeCrashed {
				crash = evt
				goto done
			}
		case <-timeout:
			t.Fatalf("timed out waiting for crash event")
		}
	}

done:
	if crash.Err == nil {
		t.Fatalf("expected crash event to include error")
	}
	if crash.Err != oomErr {
		t.Fatalf("crash event error mismatch: got %v want %v", crash.Err, oomErr)
	}
	if crash.Err.Error() != oomErr.Error() {
		t.Fatalf("crash event error message mismatch: got %q want %q", crash.Err.Error(), oomErr.Error())
	}

	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("stop supervisor: %v", err)
	}
}

func TestSupervisorStartFailuresEmitFailedEvent(t *testing.T) {
	svc := &stack.Service{
		RestartPolicy: &stack.RestartPolicy{
			MaxRetries: 1,
			Backoff: &stack.Backoff{
				Min:    stack.Duration{Duration: 10 * time.Millisecond},
				Max:    stack.Duration{Duration: 20 * time.Millisecond},
				Factor: 2,
			},
		},
	}

	inst1 := &fakeInstance{startErr: errors.New("failed to start")}
	inst2Err := errors.New("still failing")
	inst2 := &fakeInstance{startErr: inst2Err}

	events := make(chan Event, 32)
	rt := &fakeRuntime{
		instances: []*fakeInstance{inst1, inst2},
		startCh:   make(chan struct{}, 2),
	}

	sup := newSupervisor("api", 0, svc, rt, events, nil)
	sup.jitter = func(d time.Duration) time.Duration { return d }
	sup.sleep = func(ctx context.Context, d time.Duration) error { return nil }

	sup.Start(context.Background())

	waitForStart(t, rt.startCh)
	waitForStart(t, rt.startCh)

	if err := sup.AwaitReady(context.Background()); err == nil {
		t.Fatalf("expected readiness failure")
	}

	sup.Stop(context.Background())

	var crashed []Event
	var failed []Event
	for len(events) > 0 {
		evt := <-events
		if evt.Service != "api" {
			continue
		}
		switch evt.Type {
		case EventTypeCrashed:
			crashed = append(crashed, evt)
		case EventTypeFailed:
			failed = append(failed, evt)
			if !errors.Is(evt.Err, inst2Err) {
				t.Fatalf("failed event should carry final start error; got %v want %v", evt.Err, inst2Err)
			}
		}
	}

	if len(crashed) != 2 {
		t.Fatalf("expected two crashed events, got %d", len(crashed))
	}
	if crashed[0].Attempt != 1 || crashed[0].Reason != ReasonStartFailure {
		t.Fatalf("first crash metadata mismatch: %+v", crashed[0])
	}
	if crashed[1].Attempt != 2 || crashed[1].Reason != ReasonStartFailure {
		t.Fatalf("second crash metadata mismatch: %+v", crashed[1])
	}

	if len(failed) != 1 {
		t.Fatalf("expected one failed event, got %d", len(failed))
	}
	if failed[0].Attempt != 2 || failed[0].Reason != ReasonRetriesExhaust {
		t.Fatalf("failed event metadata mismatch: %+v", failed[0])
	}
}

func TestSupervisorCancelDuringBackoffDeliversCancellation(t *testing.T) {
	svc := &stack.Service{
		RestartPolicy: &stack.RestartPolicy{
			MaxRetries: 3,
			Backoff: &stack.Backoff{
				Min:    stack.Duration{Duration: 10 * time.Millisecond},
				Max:    stack.Duration{Duration: 20 * time.Millisecond},
				Factor: 2,
			},
		},
	}

	readyErr := errors.New("not ready")
	inst := &fakeInstance{waitCh: make(chan error, 1)}
	inst.waitCh <- readyErr
	rt := &fakeRuntime{
		instances: []*fakeInstance{inst},
		startCh:   make(chan struct{}, 1),
	}

	sup := newSupervisor("api", 0, svc, rt, nil, nil)
	sup.jitter = func(d time.Duration) time.Duration { return d }

	sleepCalled := make(chan struct{})
	var once sync.Once
	sup.sleep = func(ctx context.Context, d time.Duration) error {
		once.Do(func() { close(sleepCalled) })
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
			return nil
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sup.Start(ctx)

	waitForStart(t, rt.startCh)

	readyErrCh := make(chan error, 1)
	go func() {
		readyErrCh <- sup.AwaitReady(context.Background())
	}()

	select {
	case <-sleepCalled:
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for backoff sleep")
	}

	cancel()

	select {
	case err := <-readyErrCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("await ready did not return after cancellation")
	}

	if err := sup.Stop(context.Background()); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("stop supervisor: %v", err)
	}
}

func containsSequence(events []EventType, seq []EventType) bool {
	if len(seq) == 0 {
		return true
	}
	idx := 0
	for _, t := range events {
		if t == seq[idx] {
			idx++
			if idx == len(seq) {
				return true
			}
		}
	}
	return false
}

func waitForStart(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timeout waiting for runtime start")
	}
}

type fakeRuntime struct {
	mu        sync.Mutex
	instances []*fakeInstance
	startCh   chan struct{}
}

func (f *fakeRuntime) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Handle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.instances) == 0 {
		return nil, errors.New("no instances configured")
	}
	inst := f.instances[0]
	f.instances = f.instances[1:]
	if f.startCh != nil {
		f.startCh <- struct{}{}
	}
	if inst.startErr != nil {
		return nil, inst.startErr
	}
	return inst, nil
}

type fakeInstance struct {
	waitErr error
	waitCh  chan error

	healthCh chan probe.State
	logsCh   chan runtime.LogEntry

	stopErr  error
	startErr error
	stopped  chan struct{}
}

func (f *fakeInstance) WaitReady(ctx context.Context) error {
	if f.waitCh != nil {
		select {
		case err := <-f.waitCh:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.waitErr != nil {
		return f.waitErr
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (f *fakeInstance) Wait(ctx context.Context) error {
	if f.waitErr != nil {
		return f.waitErr
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeInstance) Health() <-chan probe.State {
	return f.healthCh
}

func (f *fakeInstance) Stop(ctx context.Context) error {
	if f.stopErr != nil {
		return f.stopErr
	}
	if f.healthCh != nil {
		close(f.healthCh)
		f.healthCh = nil
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			if err := ctx.Err(); err != nil {
				return err
			}
		default:
		}
	}
	if f.stopped != nil {
		select {
		case f.stopped <- struct{}{}:
		default:
		}
	}
	return nil
}

func (f *fakeInstance) Kill(ctx context.Context) error {
	return f.Stop(ctx)
}

func (f *fakeInstance) Logs(ctx context.Context) (<-chan runtime.LogEntry, error) {
	return f.logsCh, nil
}

func waitForHookPhase(t *testing.T, exec *fakeHookExecutor, phase string) {
	t.Helper()
	select {
	case got := <-exec.callCh:
		if got != phase {
			t.Fatalf("unexpected hook phase: got %s want %s", got, phase)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s hook", phase)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type fakeHookExecutor struct {
	mu      sync.Mutex
	phases  []string
	results map[string]hookResult
	blocks  map[string]chan struct{}
	callCh  chan string
}

func newFakeHookExecutor() *fakeHookExecutor {
	return &fakeHookExecutor{
		results: make(map[string]hookResult),
		blocks:  make(map[string]chan struct{}),
		callCh:  make(chan string, 16),
	}
}

func (f *fakeHookExecutor) Run(ctx context.Context, svc *stack.Service, phase string, hook *stack.LifecycleHook) hookResult {
	f.mu.Lock()
	res, ok := f.results[phase]
	if !ok {
		res = hookResult{Phase: phase}
	}
	copyRes := res
	if copyRes.Phase == "" {
		copyRes.Phase = phase
	}
	if len(copyRes.Command) == 0 && hook != nil {
		copyRes.Command = append([]string(nil), hook.Command...)
	} else if len(copyRes.Command) > 0 {
		copyRes.Command = append([]string(nil), copyRes.Command...)
	}
	if len(copyRes.Logs) > 0 {
		copyRes.Logs = append([]hookLog(nil), copyRes.Logs...)
	}
	f.phases = append(f.phases, phase)
	block := f.blocks[phase]
	f.mu.Unlock()

	select {
	case f.callCh <- phase:
	default:
	}

	if block != nil {
		<-block
	}
	return copyRes
}

func (f *fakeHookExecutor) setResult(phase string, res hookResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.results == nil {
		f.results = make(map[string]hookResult)
	}
	if res.Phase == "" {
		res.Phase = phase
	}
	f.results[phase] = res
}

func (f *fakeHookExecutor) setBlock(phase string, ch chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.blocks == nil {
		f.blocks = make(map[string]chan struct{})
	}
	if ch == nil {
		delete(f.blocks, phase)
	} else {
		f.blocks[phase] = ch
	}
}

func (f *fakeHookExecutor) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.phases))
	copy(out, f.phases)
	return out
}

func TestSupervisorManualRestart(t *testing.T) {
	svc := &stack.Service{}

	first := &fakeInstance{waitCh: make(chan error, 1)}
	second := &fakeInstance{waitCh: make(chan error, 1)}

	rt := &fakeRuntime{instances: []*fakeInstance{first, second}, startCh: make(chan struct{}, 4)}

	events := make(chan Event, 32)
	sup := newSupervisor("api", 0, svc, rt, events, nil)
	sup.jitter = func(d time.Duration) time.Duration { return d }
	sup.sleep = func(ctx context.Context, d time.Duration) error { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sup.Start(ctx)

	waitForStart(t, rt.startCh)

	first.waitCh <- nil

	if err := sup.AwaitReady(context.Background()); err != nil {
		t.Fatalf("await ready: %v", err)
	}

	restartDone := make(chan error, 1)
	go func() {
		restartDone <- sup.Restart(context.Background())
	}()

	waitForStart(t, rt.startCh)

	second.waitCh <- nil

	select {
	case err := <-restartDone:
		if err != nil {
			t.Fatalf("manual restart returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for manual restart to complete")
	}

	foundStopping := false
	foundStarting := false
	foundReady := false
	deadline := time.After(200 * time.Millisecond)
collect:
	for {
		select {
		case evt := <-events:
			if evt.Service != "api" || evt.Replica != 0 {
				continue
			}
			switch evt.Type {
			case EventTypeStopping:
				foundStopping = true
			case EventTypeStarting:
				if evt.Attempt == 2 {
					foundStarting = true
				}
			case EventTypeReady:
				if evt.Attempt == 2 {
					foundReady = true
				}
			}
			if foundStopping && foundStarting && foundReady {
				break collect
			}
		case <-deadline:
			break collect
		}
	}

	if !foundStopping {
		t.Fatalf("expected stopping event during manual restart")
	}
	if !foundStarting {
		t.Fatalf("expected second start attempt during manual restart")
	}
	if !foundReady {
		t.Fatalf("expected ready event for restart attempt")
	}

	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("stop supervisor: %v", err)
	}
}

func TestSupervisorHooksOrdering(t *testing.T) {
	svc := &stack.Service{
		Hooks: &stack.ServiceHooks{
			PreStart:  &stack.LifecycleHook{Command: []string{"true"}},
			PostStart: &stack.LifecycleHook{Command: []string{"true"}},
			PreStop:   &stack.LifecycleHook{Command: []string{"true"}},
			PostStop:  &stack.LifecycleHook{Command: []string{"true"}},
		},
	}

	inst := &fakeInstance{waitCh: make(chan error, 1), stopped: make(chan struct{}, 1)}
	rt := &fakeRuntime{instances: []*fakeInstance{inst}, startCh: make(chan struct{}, 4)}

	events := make(chan Event, 32)
	sup := newSupervisor("svc", 0, svc, rt, events, nil)
	sup.jitter = func(d time.Duration) time.Duration { return d }
	sup.sleep = func(ctx context.Context, d time.Duration) error { return nil }

	hooks := newFakeHookExecutor()
	preStartGate := make(chan struct{})
	hooks.setBlock("preStart", preStartGate)
	preStopGate := make(chan struct{})
	hooks.setBlock("preStop", preStopGate)
	sup.hooks = hooks

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)

	waitForHookPhase(t, hooks, "preStart")

	select {
	case <-rt.startCh:
		t.Fatalf("runtime start invoked before preStart hook completed")
	default:
	}

	close(preStartGate)

	waitForStart(t, rt.startCh)

	if calls := hooks.calls(); len(calls) != 1 || calls[0] != "preStart" {
		t.Fatalf("unexpected hook sequence before readiness: %v", calls)
	}

	inst.waitCh <- nil

	if err := sup.AwaitReady(context.Background()); err != nil {
		t.Fatalf("await ready: %v", err)
	}

	waitForHookPhase(t, hooks, "postStart")

	stopErrCh := make(chan error, 1)
	go func() {
		stopErrCh <- sup.Stop(context.Background())
	}()

	waitForHookPhase(t, hooks, "preStop")

	select {
	case <-inst.stopped:
		t.Fatalf("instance stopped before preStop hook completed")
	default:
	}

	close(preStopGate)

	select {
	case <-inst.stopped:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for instance stop")
	}

	waitForHookPhase(t, hooks, "postStop")

	if err := <-stopErrCh; err != nil {
		t.Fatalf("stop returned error: %v", err)
	}

	expected := []string{"preStart", "postStart", "preStop", "postStop"}
	if got := hooks.calls(); !equalStrings(got, expected) {
		t.Fatalf("unexpected hook invocation order: got %v, want %v", got, expected)
	}
}

func TestSupervisorPreStartHookTimeout(t *testing.T) {
	svc := &stack.Service{
		Hooks: &stack.ServiceHooks{
			PreStart: &stack.LifecycleHook{Command: []string{"true"}},
		},
		RestartPolicy: &stack.RestartPolicy{MaxRetries: 0},
	}

	rt := &fakeRuntime{startCh: make(chan struct{}, 1)}
	events := make(chan Event, 32)
	sup := newSupervisor("api", 0, svc, rt, events, nil)
	sup.jitter = func(d time.Duration) time.Duration { return d }
	sup.sleep = func(ctx context.Context, d time.Duration) error { return nil }

	hooks := newFakeHookExecutor()
	hooks.setResult("preStart", hookResult{
		Phase:    "preStart",
		Command:  []string{"true"},
		Err:      context.DeadlineExceeded,
		TimedOut: true,
	})
	sup.hooks = hooks

	sup.Start(context.Background())

	readyCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := sup.AwaitReady(readyCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("await ready error = %v, want deadline exceeded", err)
	}

	select {
	case <-rt.startCh:
		t.Fatalf("runtime start invoked despite preStart hook failure")
	default:
	}

	var hookEvt Event
	collected := make([]Event, 0, 8)
	deadline := time.After(time.Second)
waitHookEvent:
	for {
		select {
		case evt := <-events:
			collected = append(collected, evt)
			if evt.Source == "hook" && evt.Type == EventTypeError {
				hookEvt = evt
				break waitHookEvent
			}
		case <-deadline:
			t.Fatalf("timed out waiting for hook error event; got %v", collected)
		}
	}

	if hookEvt.Reason != ReasonHookFailed {
		t.Fatalf("unexpected hook event reason: %s", hookEvt.Reason)
	}
	if !strings.Contains(hookEvt.Message, "preStart hook timed out") {
		t.Fatalf("unexpected hook event message: %q", hookEvt.Message)
	}
	if !errors.Is(hookEvt.Err, context.DeadlineExceeded) {
		t.Fatalf("unexpected hook event error: %v", hookEvt.Err)
	}

	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("stop supervisor: %v", err)
	}
}

func TestSupervisorPreStopHookErrorEvent(t *testing.T) {
	svc := &stack.Service{
		Hooks: &stack.ServiceHooks{
			PreStart: &stack.LifecycleHook{Command: []string{"true"}},
			PreStop:  &stack.LifecycleHook{Command: []string{"true"}},
		},
	}

	inst := &fakeInstance{waitCh: make(chan error, 1), stopped: make(chan struct{}, 1)}
	rt := &fakeRuntime{instances: []*fakeInstance{inst}, startCh: make(chan struct{}, 2)}
	events := make(chan Event, 32)
	sup := newSupervisor("svc", 0, svc, rt, events, nil)
	sup.jitter = func(d time.Duration) time.Duration { return d }
	sup.sleep = func(ctx context.Context, d time.Duration) error { return nil }

	hooks := newFakeHookExecutor()
	hooks.setResult("preStop", hookResult{Phase: "preStop", Err: errors.New("cleanup failed")})
	sup.hooks = hooks

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)

	waitForStart(t, rt.startCh)
	inst.waitCh <- nil

	if err := sup.AwaitReady(context.Background()); err != nil {
		t.Fatalf("await ready: %v", err)
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- sup.Stop(context.Background()) }()

	var hookEvt Event
	collected := make([]Event, 0, 8)
	deadline := time.After(time.Second)
waitPreStopEvent:
	for {
		select {
		case evt := <-events:
			collected = append(collected, evt)
			if evt.Source == "hook" && evt.Type == EventTypeError && strings.Contains(evt.Message, "preStop hook failed") {
				hookEvt = evt
				break waitPreStopEvent
			}
		case <-deadline:
			t.Fatalf("timed out waiting for preStop hook error event; got %v", collected)
		}
	}

	if hookEvt.Err == nil || hookEvt.Err.Error() != "cleanup failed" {
		t.Fatalf("unexpected hook event error: %v", hookEvt.Err)
	}

	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("stop returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for stop to complete")
	}
}
