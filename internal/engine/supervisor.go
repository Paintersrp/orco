package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
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

type restartRequest struct {
	ctx  context.Context
	done chan error
}

// supervisor is responsible for managing the lifecycle of a single service
// instance. It runs the instance in a dedicated goroutine, observes readiness
// transitions and initiates restarts based on the configured restart policy.
type supervisor struct {
	name      string
	replica   int
	serviceMu sync.RWMutex
	service   *stack.Service
	runtime   runtime.Runtime

	events chan<- Event

	policy restartPolicy

	jitter func(time.Duration) time.Duration
	sleep  func(context.Context, time.Duration) error

	restartCh chan *restartRequest

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

func newSupervisor(name string, replica int, svc *stack.Service, rt runtime.Runtime, events chan<- Event) *supervisor {
	sup := &supervisor{
		name:      name,
		replica:   replica,
		runtime:   rt,
		events:    events,
		readyCh:   make(chan error, 1),
		existsCh:  make(chan error, 1),
		startedCh: make(chan error, 1),
		done:      make(chan struct{}),
		restartCh: make(chan *restartRequest),
	}

	sup.jitter = defaultJitter
	sup.sleep = sleepWithContext

	sup.UpdateServiceSpec(svc)

	return sup
}

func (s *supervisor) UpdateServiceSpec(spec *stack.Service) {
	s.serviceMu.Lock()
	if spec == nil {
		s.service = nil
	} else {
		s.service = spec.Clone()
	}
	s.policy = deriveRestartPolicy(s.service)
	s.serviceMu.Unlock()
}

func (s *supervisor) serviceSpec() *stack.Service {
	s.serviceMu.RLock()
	defer s.serviceMu.RUnlock()
	if s.service == nil {
		return nil
	}
	return s.service.Clone()
}

func (s *supervisor) currentPolicy() restartPolicy {
	s.serviceMu.RLock()
	defer s.serviceMu.RUnlock()
	return s.policy
}

func buildStartSpec(name string, replica int, svc *stack.Service) runtime.StartSpec {
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
	if len(svc.Volumes) > 0 {
		spec.Volumes = make([]runtime.Volume, 0, len(svc.Volumes))
		for _, mapping := range svc.Volumes {
			source, target, mode := splitResolvedVolume(mapping)
			spec.Volumes = append(spec.Volumes, runtime.Volume{
				Source: source,
				Target: target,
				Mode:   mode,
			})
		}
	}
	spec.Workdir = svc.ResolvedWorkdir
	spec.Health = svc.Health
	if svc.Resources != nil {
		spec.Resources = svc.Resources.Clone()
	}
	spec.Service = svc.Clone()
	return spec
}

func splitResolvedVolume(spec string) (source, target, mode string) {
	first := strings.Index(spec, ":")
	if first == -1 {
		return spec, "", ""
	}
	source = spec[:first]
	remainder := spec[first+1:]
	second := strings.Index(remainder, ":")
	if second == -1 {
		target = remainder
		return source, target, ""
	}
	target = remainder[:second]
	mode = remainder[second+1:]
	return source, target, mode
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

func (s *supervisor) finishRestart(req *restartRequest, err error) {
	if req == nil {
		return
	}
	if req.ctx != nil && err == nil {
		select {
		case <-req.ctx.Done():
			err = req.ctx.Err()
		default:
		}
	}
	select {
	case req.done <- err:
	default:
	}
}

func (s *supervisor) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	go s.run()
}

func (s *supervisor) Restart(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	req := &restartRequest{ctx: ctx, done: make(chan error, 1)}

	select {
	case <-s.done:
		return errors.New("supervisor not running")
	default:
	}

	select {
	case s.restartCh <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return errors.New("supervisor not running")
	}

	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		select {
		case err := <-req.done:
			return err
		default:
			return errors.New("supervisor not running")
		}
	}
}

func (s *supervisor) run() {
	defer close(s.done)

	s.deliverExists(nil)

	var pending *restartRequest
	defer func() {
		if pending != nil {
			s.finishRestart(pending, errors.New("supervisor stopped"))
		}
		for {
			select {
			case req := <-s.restartCh:
				s.finishRestart(req, errors.New("supervisor stopped"))
			default:
				return
			}
		}
	}()

	restarts := 0
	backoffBase := s.currentPolicy().min

	for {
		if err := s.ctx.Err(); err != nil {
			if pending != nil {
				s.finishRestart(pending, err)
				pending = nil
			}
			s.deliverStarted(err)
			s.deliverInitial(err)
			s.setRunErr(err)
			return
		}

		attempt := restarts + 1
		reason := ReasonInitialStart
		if attempt > 1 || pending != nil {
			reason = ReasonRestart
		}
		sendEvent(s.events, s.name, s.replica, EventTypeStarting, "starting service", attempt, reason, nil)

		spec := buildStartSpec(s.name, s.replica, s.serviceSpec())
		instance, err := s.runtime.Start(s.ctx, spec)
		if err != nil {
			if s.ctx.Err() != nil {
				err = s.ctx.Err()
			}
			if pending != nil {
				s.finishRestart(pending, err)
				pending = nil
			}
			if s.ctx.Err() != nil {
				s.deliverStarted(err)
				s.deliverInitial(err)
				s.setRunErr(err)
				return
			}

			sendEvent(s.events, s.name, s.replica, EventTypeCrashed, "start failed", attempt, ReasonStartFailure, err)
			if !s.allowRestart(restarts) {
				sendEvent(s.events, s.name, s.replica, EventTypeFailed, "service failed", attempt, ReasonRetriesExhaust, err)
				s.deliverStarted(err)
				s.deliverInitial(err)
				s.setRunErr(err)
				return
			}

			restarts++
			if pending == nil {
				if err := s.sleepBackoff(&backoffBase); err != nil {
					s.deliverStarted(err)
					s.deliverInitial(err)
					s.setRunErr(err)
					return
				}
			}
			continue
		}

		s.deliverStarted(nil)
		s.setCurrent(instance)
		instErr, ready, next := s.manageInstance(instance, attempt, pending)
		s.clearCurrent()

		if next != nil {
			pending = next
			restarts++
			backoffBase = s.currentPolicy().min
			continue
		}

		if instErr == nil {
			if pending != nil {
				s.finishRestart(pending, nil)
				pending = nil
			}
			s.setRunErr(nil)
			return
		}

		if errors.Is(instErr, context.Canceled) && s.ctx.Err() != nil {
			if pending != nil {
				s.finishRestart(pending, s.ctx.Err())
				pending = nil
			}
			s.deliverInitial(s.ctx.Err())
			s.setRunErr(s.ctx.Err())
			return
		}

		if pending != nil {
			s.finishRestart(pending, instErr)
			pending = nil
		}

		sendEvent(s.events, s.name, s.replica, EventTypeCrashed, "instance crashed", attempt, ReasonInstanceCrash, instErr)
		if !s.allowRestart(restarts) {
			sendEvent(s.events, s.name, s.replica, EventTypeFailed, "service failed", attempt, ReasonRetriesExhaust, instErr)
			if !ready {
				s.deliverInitial(instErr)
			}
			s.setRunErr(instErr)
			return
		}

		restarts++
		if pending == nil {
			if err := s.sleepBackoff(&backoffBase); err != nil {
				s.deliverStarted(err)
				s.deliverInitial(err)
				s.setRunErr(err)
				return
			}
		}
	}
}

func (s *supervisor) allowRestart(restarts int) bool {
	pol := s.currentPolicy()
	if pol.maxRetries < 0 {
		return true
	}
	return restarts < pol.maxRetries
}

func (s *supervisor) sleepBackoff(base *time.Duration) error {
	pol := s.currentPolicy()
	delay := *base
	if delay <= 0 {
		delay = pol.min
	}
	if delay > pol.max {
		delay = pol.max
	}

	jittered := s.jitter(delay)
	if jittered > pol.max {
		jittered = pol.max
	}
	if jittered < 0 {
		jittered = 0
	}

	if err := s.sleep(s.ctx, jittered); err != nil {
		return err
	}

	next := float64(delay) * pol.factor
	if math.IsInf(next, 0) || next > float64(pol.max) {
		*base = pol.max
		return nil
	}
	n := time.Duration(next)
	if n < pol.min {
		n = pol.min
	}
	if n > pol.max {
		n = pol.max
	}
	*base = n
	return nil
}

func (s *supervisor) manageInstance(instance runtime.Handle, attempt int, pending *restartRequest) (error, bool, *restartRequest) {
	var logWG sync.WaitGroup
	if instance != nil {
		logs, err := instance.Logs(s.ctx)
		if err != nil {
			sendEvent(s.events, s.name, s.replica, EventTypeError, "log stream unavailable", attempt, ReasonLogStreamError, err)
		} else if logs != nil {
			logWG.Add(1)
			go s.streamLogs(logs, &logWG, attempt)
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

	pendingRestart := pending
	var manualRestart *restartRequest

	for {
		select {
		case req := <-s.restartCh:
			if manualRestart != nil || pendingRestart != nil {
				s.finishRestart(req, errors.New("restart already in progress"))
				continue
			}
			stopCtx := req.ctx
			var cancel context.CancelFunc
			if stopCtx == nil {
				stopCtx, cancel = failureStopContext()
			}
			sendEvent(s.events, s.name, s.replica, EventTypeStopping, "stopping for restart", attempt, ReasonRestart, nil)
			err := s.stopInstance(instance, stopCtx)
			if cancel != nil {
				cancel()
			}
			if err != nil {
				s.finishRestart(req, err)
				continue
			}
			manualRestart = req
			waitCancel()
		case err := <-readyCh:
			readyCh = nil
			if err != nil {
				if pendingRestart != nil {
					s.finishRestart(pendingRestart, err)
					pendingRestart = nil
				}
				if s.ctx.Err() != nil && errors.Is(err, context.Canceled) {
					logWG.Wait()
					return s.ctx.Err(), readyObserved, nil
				}
				ctx, cancel := failureStopContext()
				_ = s.stopInstance(instance, ctx)
				cancel()
				logWG.Wait()
				return err, readyObserved, nil
			}
			readyObserved = true
			s.deliverInitial(nil)
			if pendingRestart != nil {
				s.finishRestart(pendingRestart, nil)
				pendingRestart = nil
			}
			if healthCh == nil {
				sendEvent(s.events, s.name, s.replica, EventTypeReady, "service ready", attempt, ReasonProbeReady, nil)
			}
		case state, ok := <-healthCh:
			if !ok {
				healthCh = nil
				if s.ctx.Err() != nil {
					continue
				}
				if readyObserved {
					if pendingRestart != nil {
						s.finishRestart(pendingRestart, errors.New("health channel closed"))
						pendingRestart = nil
					}
					ctx, cancel := failureStopContext()
					_ = s.stopInstance(instance, ctx)
					cancel()
					logWG.Wait()
					return errors.New("health channel closed"), readyObserved, nil
				}
				continue
			}
			switch state.Status {
			case probe.StatusReady:
				readyObserved = true
				s.deliverInitial(nil)
				if pendingRestart != nil {
					s.finishRestart(pendingRestart, nil)
					pendingRestart = nil
				}
				sendEvent(s.events, s.name, s.replica, EventTypeReady, "service ready", attempt, ReasonProbeReady, nil)
			case probe.StatusUnready:
				if !readyObserved {
					if pendingRestart != nil {
						err := state.Err
						if err == nil {
							err = errors.New("service reported unready")
						}
						s.finishRestart(pendingRestart, err)
						pendingRestart = nil
					}
					ctx, cancel := failureStopContext()
					_ = s.stopInstance(instance, ctx)
					cancel()
					logWG.Wait()
					if state.Err != nil {
						return state.Err, readyObserved, nil
					}
					return errors.New("service reported unready"), readyObserved, nil
				}
				sendEvent(s.events, s.name, s.replica, EventTypeUnready, "service unready", attempt, ReasonProbeUnready, state.Err)
				ctx, cancel := failureStopContext()
				_ = s.stopInstance(instance, ctx)
				cancel()
			}
		case err := <-waitCh:
			waitCh = nil
			if manualRestart != nil {
				logWG.Wait()
				sendEvent(s.events, s.name, s.replica, EventTypeStopped, "service stopped for restart", attempt, ReasonRestart, nil)
				return nil, readyObserved, manualRestart
			}
			if s.ctx.Err() != nil {
				if pendingRestart != nil {
					s.finishRestart(pendingRestart, s.ctx.Err())
					pendingRestart = nil
				}
				if !readyObserved {
					s.deliverInitial(s.ctx.Err())
				}
				stopCtx := s.stopContext()
				stopErr := s.stopInstance(instance, stopCtx)
				s.setStopErr(stopErr)
				logWG.Wait()
				sendEvent(s.events, s.name, s.replica, EventTypeStopped, "service stopped", attempt, ReasonSupervisorStop, nil)
				return nil, readyObserved, nil
			}
			exitErr := err
			if exitErr == nil {
				exitErr = fmt.Errorf("service %s exited unexpectedly", s.name)
			}
			if pendingRestart != nil {
				s.finishRestart(pendingRestart, exitErr)
				pendingRestart = nil
			}
			if !readyObserved {
				s.deliverInitial(exitErr)
			}
			ctx, cancel := failureStopContext()
			_ = s.stopInstance(instance, ctx)
			cancel()
			logWG.Wait()
			return exitErr, readyObserved, nil
		case <-s.ctx.Done():
			if pendingRestart != nil {
				s.finishRestart(pendingRestart, s.ctx.Err())
				pendingRestart = nil
			}
			if !readyObserved {
				s.deliverInitial(s.ctx.Err())
			}
			stopCtx := s.stopContext()
			err := s.stopInstance(instance, stopCtx)
			s.setStopErr(err)
			logWG.Wait()
			sendEvent(s.events, s.name, s.replica, EventTypeStopped, "service stopped", attempt, ReasonSupervisorStop, nil)
			return nil, readyObserved, nil
		}
	}
}

func (s *supervisor) streamLogs(logs <-chan runtime.LogEntry, wg *sync.WaitGroup, attempt int) {
	defer wg.Done()
	var dropped int
	for entry := range logs {
		if entry.Message == "" {
			continue
		}
		if dropped > 0 {
			if !s.emitDropped(dropped, false, attempt) {
				dropped++
				continue
			}
			dropped = 0
		}
		evt := s.normalizeLog(entry, attempt)
		if !s.emitLog(evt, false) {
			dropped++
		}
	}
	if dropped > 0 {
		s.emitDropped(dropped, true, attempt)
	}
}

func (s *supervisor) normalizeLog(entry runtime.LogEntry, attempt int) Event {
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
		Replica:   s.replica,
		Type:      EventTypeLog,
		Message:   entry.Message,
		Level:     level,
		Source:    source,
		Attempt:   attempt,
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

func (s *supervisor) emitDropped(count int, block bool, attempt int) bool {
	evt := Event{
		Timestamp: time.Now(),
		Service:   s.name,
		Replica:   s.replica,
		Type:      EventTypeLog,
		Message:   fmt.Sprintf("dropped=%d", count),
		Level:     "warn",
		Source:    runtime.LogSourceSystem,
		Attempt:   attempt,
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
