package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/example/orco/internal/probe"
	"github.com/example/orco/internal/runtime"
	"github.com/example/orco/internal/stack"
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
	sup := newSupervisor("web", svc, rt, events)
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
	var types []EventType
	deadline := time.After(time.Second)
	for {
		if len(types) >= 6 {
			break
		}
		select {
		case evt := <-events:
			if evt.Service != "web" {
				continue
			}
			types = append(types, evt.Type)
			if evt.Type == EventTypeReady {
				// The restart cycle completed.
				if len(types) >= 6 {
					break
				}
			}
		case <-deadline:
			t.Fatalf("timed out waiting for events; got %v", types)
		}
	}

	// Expect at least one unready, crash and a subsequent starting event.
	if !containsSequence(types, []EventType{EventTypeUnready, EventTypeCrashed, EventTypeStarting, EventTypeReady}) {
		t.Fatalf("expected restart sequence, got %v", types)
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

	var delays []time.Duration
	sup := newSupervisor("db", svc, rt, make(chan Event, 32))
	sup.jitter = func(d time.Duration) time.Duration { return d }
	sup.sleep = func(ctx context.Context, d time.Duration) error {
		delays = append(delays, d)
		return nil
	}

	sup.Start(context.Background())

	if err := sup.AwaitReady(context.Background()); err == nil {
		t.Fatalf("expected readiness failure")
	}

	sup.Stop(context.Background())

	expected := []time.Duration{
		50 * time.Millisecond,
		100 * time.Millisecond,
		200 * time.Millisecond,
	}

	if len(delays) != len(expected) {
		t.Fatalf("expected %d backoff delays, got %d (%v)", len(expected), len(delays), delays)
	}

	for i, d := range expected {
		if delays[i] != d {
			t.Fatalf("delay %d: expected %v, got %v", i, d, delays[i])
		}
	}
}

func TestSupervisorMaxRetriesEmitsCrashed(t *testing.T) {
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

	sup := newSupervisor("api", svc, rt, events)
	sup.jitter = func(d time.Duration) time.Duration { return d }
	sup.sleep = func(ctx context.Context, d time.Duration) error { return nil }

	sup.Start(context.Background())

	if err := sup.AwaitReady(context.Background()); err == nil {
		t.Fatalf("expected readiness failure")
	}

	sup.Stop(context.Background())

	found := false
	for len(events) > 0 {
		evt := <-events
		if evt.Service != "api" {
			continue
		}
		if evt.Type == EventTypeCrashed {
			found = true
		}
	}

	if !found {
		t.Fatalf("expected crashed event after exhausting retries")
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

func (f *fakeRuntime) Start(ctx context.Context, name string, svc *stack.Service) (runtime.Instance, error) {
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

func (f *fakeInstance) Health() <-chan probe.State {
	return f.healthCh
}

func (f *fakeInstance) Stop(ctx context.Context) error {
	if f.stopErr != nil {
		return f.stopErr
	}
	if f.healthCh != nil {
		close(f.healthCh)
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
	return nil
}

func (f *fakeInstance) Logs() <-chan runtime.LogEntry {
	return f.logsCh
}
