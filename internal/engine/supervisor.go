package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/Paintersrp/orco/internal/probe"
	"github.com/Paintersrp/orco/internal/runtime"
	"github.com/Paintersrp/orco/internal/stack"
)

const (
	defaultBackoffMin    = time.Second
	defaultBackoffMax    = 30 * time.Second
	defaultBackoffFactor = 2.0
	instanceStopTimeout  = 5 * time.Second
)

type restartPolicy struct {
	maxRetries int
	min        time.Duration
	max        time.Duration
	factor     float64
}

// supervisor is responsible for managing the lifecycle of a single service
// instance. It runs the instance in a dedicated goroutine, observes readiness
// transitions and initiates restarts based on the configured restart policy.
type supervisor struct {
	name    string
	service *stack.Service
	runtime runtime.Runtime

	events chan<- Event

	policy restartPolicy

	jitter func(time.Duration) time.Duration
	sleep  func(context.Context, time.Duration) error

	readyOnce sync.Once
	readyCh   chan error

	existsOnce  sync.Once
	existsCh    chan error
	startedOnce sync.Once
	startedCh   chan error

	ctx    context.Context
	cancel context.CancelFunc

	done chan struct{}

	mu      sync.Mutex
	current runtime.Handle
	stopCtx context.Context
	stopErr error
	runErr  error

	stopOnce sync.Once
}

func newSupervisor(name string, svc *stack.Service, rt runtime.Runtime, events chan<- Event) *supervisor {
	sup := &supervisor{
		name:      name,
		service:   svc,
		runtime:   rt,
		events:    events,
		readyCh:   make(chan error, 1),
		existsCh:  make(chan error, 1),
		startedCh: make(chan error, 1),
		done:      make(chan struct{}),
	}

	sup.policy = deriveRestartPolicy(svc)
	sup.jitter = defaultJitter
	sup.sleep = sleepWithContext

	return sup
}

func buildStartSpec(name string, svc *stack.Service) runtime.StartSpec {
	spec := runtime.StartSpec{Name: name}
	if svc == nil {
		return spec
	}
	spec.Image = svc.Image
	if len(svc.Command) > 0 {
		spec.Command = append([]string(nil), svc.Command...)
	}
	if len(svc.Env) > 0 {
		env := make(map[string]string, len(svc.Env))
		for k, v := range svc.Env {
			env[k] = v
		}
		spec.Env = env
	}
	if len(svc.Ports) > 0 {
		spec.Ports = append([]string(nil), svc.Ports...)
	}
	spec.Workdir = svc.ResolvedWorkdir
	spec.Health = svc.Health
	spec.Service = svc.Clone()
	return spec
}

func deriveRestartPolicy(svc *stack.Service) restartPolicy {
	pol := restartPolicy{maxRetries: 3, min: defaultBackoffMin, max: defaultBackoffMax, factor: defaultBackoffFactor}
	if svc == nil || svc.RestartPolicy == nil {
		return pol
	}

	rp := svc.RestartPolicy
	switch {
	case rp.MaxRetries < 0:
		pol.maxRetries = -1
	case rp.MaxRetries == 0:
		pol.maxRetries = 0
	default:
		pol.maxRetries = rp.MaxRetries
	}
	if rp.Backoff != nil {
		if rp.Backoff.Min.Duration > 0 {
			pol.min = rp.Backoff.Min.Duration
		}
		if rp.Backoff.Max.Duration > 0 {
			pol.max = rp.Backoff.Max.Duration
		}
		if rp.Backoff.Factor > 0 {
			pol.factor = rp.Backoff.Factor
		}
	}

	if pol.min <= 0 {
		pol.min = defaultBackoffMin
	}
	if pol.max <= 0 {
		pol.max = defaultBackoffMax
	}
	if pol.max < pol.min {
		pol.max = pol.min
	}
	if pol.factor <= 1 {
		pol.factor = defaultBackoffFactor
	}

	return pol
}

func defaultJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	// Full jitter: random duration in [0, d].
	return time.Duration(rand.Float64() * float64(d))
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *supervisor) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	go s.run()
}

func (s *supervisor) run() {
	defer close(s.done)

	s.deliverExists(nil)

	restarts := 0
	backoffBase := s.policy.min

	for {
		if err := s.ctx.Err(); err != nil {
			s.deliverStarted(err)
			s.deliverInitial(err)
			s.setRunErr(err)
			return
		}

		sendEvent(s.events, s.name, EventTypeStarting, "starting service", nil)

		spec := buildStartSpec(s.name, s.service)
		instance, err := s.runtime.Start(s.ctx, spec)
		if err != nil {
			if s.ctx.Err() != nil {
				s.deliverStarted(s.ctx.Err())
				s.deliverInitial(s.ctx.Err())
				s.setRunErr(s.ctx.Err())
				return
			}

			sendEvent(s.events, s.name, EventTypeCrashed, "start failed", err)
			if !s.allowRestart(restarts) {
				sendEvent(s.events, s.name, EventTypeFailed, "service failed", err)
				s.deliverStarted(err)
				s.deliverInitial(err)
				s.setRunErr(err)
				return
			}

			restarts++
			if err := s.sleepBackoff(&backoffBase); err != nil {
				s.deliverStarted(err)
				s.deliverInitial(err)
				s.setRunErr(err)
				return
			}
			continue
		}

		s.deliverStarted(nil)
		s.setCurrent(instance)
		instErr, ready := s.manageInstance(instance)
		s.clearCurrent()

		if instErr == nil {
			s.setRunErr(nil)
			return
		}

		if errors.Is(instErr, context.Canceled) && s.ctx.Err() != nil {
			s.deliverInitial(s.ctx.Err())
			s.setRunErr(s.ctx.Err())
			return
		}

		sendEvent(s.events, s.name, EventTypeCrashed, "instance crashed", instErr)
		if !s.allowRestart(restarts) {
			sendEvent(s.events, s.name, EventTypeFailed, "service failed", instErr)
			if !ready {
				s.deliverInitial(instErr)
			}
			s.setRunErr(instErr)
			return
		}

		restarts++
		if err := s.sleepBackoff(&backoffBase); err != nil {
			s.deliverStarted(err)
			s.deliverInitial(err)
			s.setRunErr(err)
			return
		}
	}
}

func (s *supervisor) allowRestart(restarts int) bool {
	if s.policy.maxRetries < 0 {
		return true
	}
	return restarts < s.policy.maxRetries
}

func (s *supervisor) sleepBackoff(base *time.Duration) error {
	delay := *base
	if delay <= 0 {
		delay = s.policy.min
	}
	if delay > s.policy.max {
		delay = s.policy.max
	}

	jittered := s.jitter(delay)
	if jittered > s.policy.max {
		jittered = s.policy.max
	}
	if jittered < 0 {
		jittered = 0
	}

	if err := s.sleep(s.ctx, jittered); err != nil {
		return err
	}

	next := float64(delay) * s.policy.factor
	if math.IsInf(next, 0) || next > float64(s.policy.max) {
		*base = s.policy.max
		return nil
	}
	n := time.Duration(next)
	if n < s.policy.min {
		n = s.policy.min
	}
	if n > s.policy.max {
		n = s.policy.max
	}
	*base = n
	return nil
}

func (s *supervisor) manageInstance(instance runtime.Handle) (error, bool) {
	var logWG sync.WaitGroup
	if instance != nil {
		logs, err := instance.Logs(s.ctx)
		if err != nil {
			sendEvent(s.events, s.name, EventTypeError, "log stream unavailable", err)
		} else if logs != nil {
			logWG.Add(1)
			go s.streamLogs(logs, &logWG)
		}
	}

	readyCh := make(chan error, 1)
	go func() {
		readyCh <- instance.WaitReady(s.ctx)
	}()

	healthCh := instance.Health()
	readyObserved := false

	waitCh := make(chan error, 1)
	waitCtx, waitCancel := context.WithCancel(context.Background())
	go func() {
		waitCh <- instance.Wait(waitCtx)
	}()
	defer waitCancel()

	for {
		select {
		case err := <-readyCh:
			readyCh = nil
			if err != nil {
				if s.ctx.Err() != nil && errors.Is(err, context.Canceled) {
					logWG.Wait()
					return s.ctx.Err(), readyObserved
				}
				ctx, cancel := failureStopContext()
				_ = s.stopInstance(instance, ctx)
				cancel()
				logWG.Wait()
				return err, readyObserved
			}
			readyObserved = true
			s.deliverInitial(nil)
			if healthCh == nil {
				sendEvent(s.events, s.name, EventTypeReady, "service ready", nil)
			}
		case state, ok := <-healthCh:
			if !ok {
				healthCh = nil
				if s.ctx.Err() != nil {
					continue
				}
				if readyObserved {
					ctx, cancel := failureStopContext()
					_ = s.stopInstance(instance, ctx)
					cancel()
					logWG.Wait()
					return errors.New("health channel closed"), readyObserved
				}
				continue
			}
			switch state.Status {
			case probe.StatusReady:
				readyObserved = true
				s.deliverInitial(nil)
				sendEvent(s.events, s.name, EventTypeReady, "service ready", nil)
			case probe.StatusUnready:
				if !readyObserved {
					ctx, cancel := failureStopContext()
					_ = s.stopInstance(instance, ctx)
					cancel()
					logWG.Wait()
					if state.Err != nil {
						return state.Err, readyObserved
					}
					return errors.New("service reported unready"), readyObserved
				}
				sendEvent(s.events, s.name, EventTypeUnready, "service unready", state.Err)
				ctx, cancel := failureStopContext()
				_ = s.stopInstance(instance, ctx)
				cancel()
				logWG.Wait()
				if state.Err != nil {
					return state.Err, readyObserved
				}
				return errors.New("service reported unready"), readyObserved
			}
		case err := <-waitCh:
			waitCh = nil
			if s.ctx.Err() != nil {
				if !readyObserved {
					s.deliverInitial(s.ctx.Err())
				}
				stopCtx := s.stopContext()
				stopErr := s.stopInstance(instance, stopCtx)
				s.setStopErr(stopErr)
				logWG.Wait()
				sendEvent(s.events, s.name, EventTypeStopped, "service stopped", nil)
				return nil, readyObserved
			}
			exitErr := err
			if exitErr == nil {
				exitErr = fmt.Errorf("service %s exited unexpectedly", s.name)
			}
			if !readyObserved {
				s.deliverInitial(exitErr)
			}
			ctx, cancel := failureStopContext()
			_ = s.stopInstance(instance, ctx)
			cancel()
			logWG.Wait()
			return exitErr, readyObserved
		case <-s.ctx.Done():
			if !readyObserved {
				s.deliverInitial(s.ctx.Err())
			}
			stopCtx := s.stopContext()
			err := s.stopInstance(instance, stopCtx)
			s.setStopErr(err)
			logWG.Wait()
			sendEvent(s.events, s.name, EventTypeStopped, "service stopped", nil)
			return nil, readyObserved
		}
	}
}

func (s *supervisor) streamLogs(logs <-chan runtime.LogEntry, wg *sync.WaitGroup) {
	defer wg.Done()
	var dropped int
	for entry := range logs {
		if entry.Message == "" {
			continue
		}
		if dropped > 0 {
			if !s.emitDropped(dropped, false) {
				dropped++
				continue
			}
			dropped = 0
		}
		evt := s.normalizeLog(entry)
		if !s.emitLog(evt, false) {
			dropped++
		}
	}
	if dropped > 0 {
		s.emitDropped(dropped, true)
	}
}

func (s *supervisor) normalizeLog(entry runtime.LogEntry) Event {
	level := entry.Level
	source := entry.Source
	if source == "" {
		source = runtime.LogSourceStdout
	}
	if level == "" {
		if source == runtime.LogSourceStderr {
			level = "warn"
		} else {
			level = "info"
		}
	}
	ts := entry.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	return Event{
		Timestamp: ts,
		Service:   s.name,
		Replica:   0,
		Type:      EventTypeLog,
		Message:   entry.Message,
		Level:     level,
		Source:    source,
	}
}

func (s *supervisor) emitLog(evt Event, block bool) bool {
	if s.events == nil {
		return true
	}
	if block {
		select {
		case s.events <- evt:
			return true
		case <-s.ctx.Done():
			return false
		}
	}
	select {
	case s.events <- evt:
		return true
	default:
		return false
	}
}

func (s *supervisor) emitDropped(count int, block bool) bool {
	evt := Event{
		Timestamp: time.Now(),
		Service:   s.name,
		Replica:   0,
		Type:      EventTypeLog,
		Message:   fmt.Sprintf("dropped=%d", count),
		Level:     "warn",
		Source:    runtime.LogSourceSystem,
	}
	return s.emitLog(evt, block)
}

func (s *supervisor) stopInstance(instance runtime.Handle, ctx context.Context) error {
	if instance == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return instance.Stop(ctx)
}

func failureStopContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), instanceStopTimeout)
}

func (s *supervisor) setCurrent(inst runtime.Handle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = inst
}

func (s *supervisor) clearCurrent() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = nil
}

func (s *supervisor) stopContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopCtx != nil {
		return s.stopCtx
	}
	return context.Background()
}

func (s *supervisor) setStopErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopErr = err
}

func (s *supervisor) getStopErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopErr
}

func (s *supervisor) setRunErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runErr = err
}

func (s *supervisor) deliverInitial(err error) {
	s.readyOnce.Do(func() {
		s.readyCh <- err
		close(s.readyCh)
	})
}

func (s *supervisor) deliverExists(err error) {
	s.existsOnce.Do(func() {
		s.existsCh <- err
		close(s.existsCh)
	})
}

func (s *supervisor) AwaitExists(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-s.existsCh:
		return err
	}
}

func (s *supervisor) deliverStarted(err error) {
	s.startedOnce.Do(func() {
		s.startedCh <- err
		close(s.startedCh)
	})
}

func (s *supervisor) AwaitStarted(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-s.startedCh:
		return err
	}
}

func (s *supervisor) AwaitReady(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-s.readyCh:
		return err
	}
}

func (s *supervisor) Stop(ctx context.Context) error {
	var result error
	s.stopOnce.Do(func() {
		alreadyDone := false
		select {
		case <-s.done:
			alreadyDone = true
		default:
		}

		s.mu.Lock()
		s.stopCtx = ctx
		s.mu.Unlock()
		if s.cancel != nil {
			s.cancel()
		}
		if ctx == nil {
			ctx = context.Background()
		}
		if alreadyDone {
			result = nil
			return
		}

		select {
		case <-s.done:
			result = s.getStopErr()
		case <-ctx.Done():
			result = ctx.Err()
		}
	})
	return result
}
