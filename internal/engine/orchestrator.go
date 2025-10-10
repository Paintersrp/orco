package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Paintersrp/orco/internal/runtime"
	proxyruntime "github.com/Paintersrp/orco/internal/runtime/proxy"
	"github.com/Paintersrp/orco/internal/stack"
)

// Orchestrator coordinates runtime adapters to bring services up respecting the
// dependency DAG.
type Orchestrator struct {
	runtimes runtime.Registry
}

// ErrNoPromotionPending indicates that a service is not waiting on a manual promotion signal.
var ErrNoPromotionPending = errors.New("service has no pending promotion")

// NewOrchestrator constructs an orchestrator backed by the provided runtime
// registry.
func NewOrchestrator(reg runtime.Registry) *Orchestrator {
	return &Orchestrator{runtimes: reg.Clone()}
}

// Deployment tracks state for services started by the orchestrator.
type Deployment struct {
	handles []*serviceHandle

	stopOnce sync.Once
	stopErr  error
}

// Service represents a running service within a deployment.
type Service struct {
	handle *serviceHandle
}

// Service retrieves a handle for the named service if it is part of the deployment.
func (d *Deployment) Service(name string) (*Service, bool) {
	for _, handle := range d.handles {
		if handle.name == name {
			return &Service{handle: handle}, true
		}
	}
	return nil, false
}

// UpdateService performs a rolling update on the named service.
func (d *Deployment) UpdateService(ctx context.Context, name string, spec *stack.Service) error {
	if d == nil {
		return fmt.Errorf("deployment is nil")
	}
	for _, handle := range d.handles {
		if handle.name == name {
			return handle.update(ctx, spec)
		}
	}
	return fmt.Errorf("service %s is not part of the deployment", name)
}

// PromoteService signals the orchestrator to continue a pending canary rollout for the named service.
func (d *Deployment) PromoteService(ctx context.Context, name string) error {
	if d == nil {
		return fmt.Errorf("deployment is nil")
	}
	for _, handle := range d.handles {
		if handle.name == name {
			return handle.promote(ctx)
		}
	}
	return fmt.Errorf("service %s is not part of the deployment", name)
}

// Name returns the service identifier associated with the handle.
func (s *Service) Name() string {
	if s == nil || s.handle == nil {
		return ""
	}
	return s.handle.name
}

// Replicas returns the number of replicas managed for the service.
func (s *Service) Replicas() int {
	if s == nil || s.handle == nil {
		return 0
	}
	return len(s.handle.replicas)
}

// RestartReplica performs a rolling restart of the specified replica and waits until it reports readiness again.
func (s *Service) RestartReplica(ctx context.Context, index int) error {
	if s == nil || s.handle == nil {
		return fmt.Errorf("service handle is nil")
	}
	if index < 0 || index >= len(s.handle.replicas) {
		return fmt.Errorf("service %s replica %d not available", s.handle.name, index)
	}
	replica := s.handle.replicas[index]
	if replica == nil || replica.supervisor == nil {
		return fmt.Errorf("service %s replica %d supervisor unavailable", s.handle.name, index)
	}
	return replica.supervisor.Restart(ctx)
}

// Update performs a rolling update of the service using the provided specification.
func (s *Service) Update(ctx context.Context, spec *stack.Service) error {
	if s == nil || s.handle == nil {
		return fmt.Errorf("service handle is nil")
	}
	return s.handle.update(ctx, spec)
}

// Promote continues a pending canary rollout for the service.
func (s *Service) Promote(ctx context.Context) error {
	if s == nil || s.handle == nil {
		return fmt.Errorf("service handle is nil")
	}
	return s.handle.promote(ctx)
}

type serviceHandle struct {
	name     string
	service  *stack.Service
	replicas []*replicaHandle

	events chan<- Event

	controllerMu sync.Mutex
	controller   *updateController

	updateMu sync.Mutex
}

const (
	promotionRollbackTimeout = 5 * time.Second
	blueGreenAbortTimeout    = 2 * time.Second
)

type replicaHandle struct {
	index      int
	supervisor *supervisor

	existsOnce sync.Once
	existsErr  error

	startedOnce sync.Once
	startedErr  error

	readyOnce sync.Once
	readyErr  error
}

func (h *serviceHandle) awaitExists(ctx context.Context) error {
	for _, replica := range h.replicas {
		if err := replica.awaitExists(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (h *serviceHandle) awaitStarted(ctx context.Context) error {
	for _, replica := range h.replicas {
		if err := replica.awaitStarted(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (h *serviceHandle) awaitReady(ctx context.Context) error {
	for _, replica := range h.replicas {
		if err := replica.awaitReady(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *replicaHandle) awaitFailure(ctx context.Context) error {
	return r.supervisor.AwaitFailure(ctx)
}

func (h *serviceHandle) update(ctx context.Context, svc *stack.Service) (retErr error) {
	if h == nil {
		return fmt.Errorf("service handle is nil")
	}
	if svc == nil {
		return fmt.Errorf("service %s spec is nil", h.name)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	h.updateMu.Lock()
	defer h.updateMu.Unlock()

	if len(h.replicas) == 0 {
		h.service = svc.Clone()
		return nil
	}

	newSpec := svc.Clone()
	var previous *stack.Service
	if h.service != nil {
		previous = h.service.Clone()
	}

	strategy := "rolling"
	if newSpec.Update != nil && strings.TrimSpace(newSpec.Update.Strategy) != "" {
		strategy = strings.ToLower(strings.TrimSpace(newSpec.Update.Strategy))
	}

	if strategy == "bluegreen" {
		return h.updateBlueGreen(ctx, newSpec, previous)
	}

	controller := newUpdateController()
	if err := h.installController(controller); err != nil {
		return err
	}
	defer func() {
		controller.finish(retErr)
		h.clearController(controller)
	}()

	return h.updateRolling(ctx, controller, newSpec, previous)
}

func (h *serviceHandle) updateRolling(ctx context.Context, controller *updateController, newSpec, previous *stack.Service) (retErr error) {
	if controller == nil {
		return fmt.Errorf("service %s update controller is nil", h.name)
	}

	updateCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var monitor *updateFailureMonitor
	if threshold := abortAfterFailures(newSpec); threshold > 0 {
		monitor = newUpdateFailureMonitor(updateCtx, cancel, threshold, observationWindowDuration(newSpec))
		defer monitor.Stop()
	}

	canary := h.replicas[0]
	if canary == nil || canary.supervisor == nil {
		return fmt.Errorf("service %s replica %d supervisor unavailable", h.name, 0)
	}

	updated := make([]*replicaHandle, 0, len(h.replicas))
	updated = append(updated, canary)

	canary.supervisor.UpdateServiceSpec(newSpec)
	if monitor != nil {
		monitor.Track(canary)
	}
	if err := canary.supervisor.Restart(updateCtx); err != nil {
		if msg, cause, aborted := monitorResult(monitor, err); aborted {
			retErr = h.abortUpdateWithMessage(updated, previous, msg, cause)
		} else {
			retErr = h.abortUpdate(updated, previous, err)
		}
		return retErr
	}

	if msg, cause, aborted := monitorResult(monitor, nil); aborted {
		retErr = h.abortUpdateWithMessage(updated, previous, msg, cause)
		return retErr
	}

	sendEvent(h.events, h.name, canary.index, EventTypeCanary, "canary replica ready", 0, ReasonCanary, nil)

	if promoteAfter := promoteAfterDuration(newSpec); promoteAfter > 0 {
		controller.startAutoPromote(promoteAfter)
	}

	if err := controller.wait(updateCtx); err != nil {
		if msg, cause, aborted := monitorResult(monitor, err); aborted {
			retErr = h.abortUpdateWithMessage(updated, previous, msg, cause)
		} else {
			retErr = h.abortUpdate(updated, previous, err)
		}
		return retErr
	}

	for _, replica := range h.replicas[1:] {
		if msg, cause, aborted := monitorResult(monitor, nil); aborted {
			retErr = h.abortUpdateWithMessage(updated, previous, msg, cause)
			return retErr
		}
		if replica == nil || replica.supervisor == nil {
			retErr = h.abortUpdate(updated, previous, fmt.Errorf("service %s replica %d supervisor unavailable", h.name, replica.index))
			return retErr
		}
		replica.supervisor.UpdateServiceSpec(newSpec)
		if monitor != nil {
			monitor.Track(replica)
		}
		updated = append(updated, replica)
		if err := replica.supervisor.Restart(updateCtx); err != nil {
			if msg, cause, aborted := monitorResult(monitor, err); aborted {
				retErr = h.abortUpdateWithMessage(updated, previous, msg, cause)
			} else {
				retErr = h.abortUpdate(updated, previous, err)
			}
			return retErr
		}
		if msg, cause, aborted := monitorResult(monitor, nil); aborted {
			retErr = h.abortUpdateWithMessage(updated, previous, msg, cause)
			return retErr
		}
	}

	if msg, cause, aborted := monitorResult(monitor, nil); aborted {
		retErr = h.abortUpdateWithMessage(updated, previous, msg, cause)
		return retErr
	}

	h.service = newSpec
	sendEvent(h.events, h.name, -1, EventTypePromoted, "promotion complete", 0, ReasonPromoted, nil)
	return nil
}

func (h *serviceHandle) updateBlueGreen(ctx context.Context, newSpec, previous *stack.Service) error {
	if len(h.replicas) == 0 {
		h.service = newSpec
		return nil
	}

	blue := append([]*replicaHandle(nil), h.replicas...)
	green := make([]*replicaHandle, len(blue))

	sendEvent(h.events, h.name, -1, EventTypeUpdatePhase, blueGreenPhaseMessage(BlueGreenPhaseProvisionGreen), 0, ReasonBlueGreenProvision, nil)

	for i, replica := range blue {
		if replica == nil || replica.supervisor == nil {
			return fmt.Errorf("service %s replica %d supervisor unavailable", h.name, i)
		}
		sup := newSupervisor(h.name, replica.index, newSpec, replica.supervisor.runtime, h.events, replica.supervisor.proxy)
		green[i] = &replicaHandle{index: replica.index, supervisor: sup}
		sup.Start(ctx)
	}

	sendEvent(h.events, h.name, -1, EventTypeUpdatePhase, blueGreenPhaseMessage(BlueGreenPhaseVerify), 0, ReasonBlueGreenVerify, nil)

	for _, replica := range green {
		if err := replica.awaitReady(ctx); err != nil {
			shutdownReplicaSet(ctx, green)
			cause := fmt.Errorf("green replica %d readiness failed: %w", replica.index, err)
			sendEvent(h.events, h.name, replica.index, EventTypeAborted, "blue-green update aborted", 0, ReasonBlueGreenRollback, cause)
			return fmt.Errorf("service %s blue-green verification failed: %w", h.name, cause)
		}
	}

	rollbackWindow := time.Duration(0)
	drainTimeout := time.Duration(0)
	switchMode := stack.BlueGreenSwitchPorts
	if newSpec.Update != nil && newSpec.Update.BlueGreen != nil {
		rollbackWindow = newSpec.Update.BlueGreen.RollbackWindow.Duration
		drainTimeout = newSpec.Update.BlueGreen.DrainTimeout.Duration
		if mode := strings.TrimSpace(newSpec.Update.BlueGreen.Switch); mode != "" {
			normalized, err := normalizeBlueGreenSwitch(mode)
			if err != nil {
				shutdownReplicaSet(ctx, green)
				return fmt.Errorf("service %s blue-green switch: %w", h.name, err)
			}
			switchMode = normalized
		}
	}

	cutoverMsg := blueGreenPhaseMessage(BlueGreenPhaseCutover)
	if switchMode != "" {
		cutoverMsg = fmt.Sprintf("%s (switch=%s)", cutoverMsg, switchMode)
	}
	sendEvent(h.events, h.name, -1, EventTypeUpdatePhase, cutoverMsg, 0, ReasonBlueGreenCutover, nil)

	h.replicas = green
	h.service = newSpec
	sendEvent(h.events, h.name, -1, EventTypePromoted, "blue-green cutover complete", 0, ReasonPromoted, nil)

	if rollbackWindow > 0 {
		rollbackTimer := time.NewTimer(rollbackWindow)
		defer rollbackTimer.Stop()

		failureCtx, failureCancel := context.WithCancel(context.Background())
		failureCh := make(chan error, 1)
		var failureWG sync.WaitGroup
		for _, replica := range green {
			if replica == nil || replica.supervisor == nil {
				continue
			}
			failureWG.Add(1)
			go func(rep *replicaHandle) {
				defer failureWG.Done()
				if err := rep.awaitFailure(failureCtx); err != nil && !errors.Is(err, context.Canceled) {
					select {
					case failureCh <- err:
					default:
					}
				}
			}(replica)
		}

		var rollbackErr error
		select {
		case <-rollbackTimer.C:
		case <-ctx.Done():
			rollbackErr = ctx.Err()
		case err := <-failureCh:
			rollbackErr = err
		}

		failureCancel()
		failureWG.Wait()

		if rollbackErr != nil {
			shutdownReplicaSet(ctx, green)
			h.replicas = blue
			if previous != nil {
				h.service = previous
			}
			err := fmt.Errorf("service %s blue-green rollback triggered: %w", h.name, rollbackErr)
			sendEvent(h.events, h.name, -1, EventTypeAborted, "blue-green rollback initiated", 0, ReasonBlueGreenRollback, err)
			return err
		}
	}

	sendEvent(h.events, h.name, -1, EventTypeUpdatePhase, blueGreenPhaseMessage(BlueGreenPhaseDecommission), 0, ReasonBlueGreenDecommission, nil)
	if err := h.stopReplicaSet(ctx, blue, drainTimeout); err != nil {
		return err
	}
	return nil
}

func (h *serviceHandle) start(ctx context.Context) {
	for _, replica := range h.replicas {
		replica.supervisor.Start(ctx)
	}
}

func (h *serviceHandle) stop(ctx context.Context, events chan<- Event) error {
	var firstErr error
	for i := len(h.replicas) - 1; i >= 0; i-- {
		replica := h.replicas[i]
		sendEvent(events, h.name, replica.index, EventTypeStopping, "stopping service", 0, ReasonShutdown, nil)
		if err := replica.supervisor.Stop(ctx); err != nil {
			sendEvent(events, h.name, replica.index, EventTypeError, "stop failed", 0, ReasonStopFailed, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("stop service %s replica %d: %w", h.name, replica.index, err)
			}
		}
	}
	return firstErr
}

func (h *serviceHandle) stopReplicaSet(_ context.Context, replicas []*replicaHandle, drainTimeout time.Duration) error {
	baseCtx := context.Background()
	var firstErr error
	for i := len(replicas) - 1; i >= 0; i-- {
		replica := replicas[i]
		if replica == nil || replica.supervisor == nil {
			continue
		}
		sendEvent(h.events, h.name, replica.index, EventTypeStopping, "stopping service", 0, ReasonShutdown, nil)
		stopCtx := baseCtx
		var cancel context.CancelFunc
		if drainTimeout > 0 {
			stopCtx, cancel = context.WithTimeout(baseCtx, drainTimeout)
		}
		if err := replica.supervisor.Stop(stopCtx); err != nil {
			sendEvent(h.events, h.name, replica.index, EventTypeError, "stop failed", 0, ReasonStopFailed, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("stop service %s replica %d: %w", h.name, replica.index, err)
			}
		}
		if cancel != nil {
			cancel()
		}
	}
	return firstErr
}

func shutdownReplicaSet(_ context.Context, replicas []*replicaHandle) {
	for _, replica := range replicas {
		if replica == nil || replica.supervisor == nil {
			continue
		}
		stopCtx := context.Background()
		stopCtx, cancel := context.WithTimeout(stopCtx, blueGreenAbortTimeout)
		_ = replica.supervisor.Stop(stopCtx)
		if cancel != nil {
			cancel()
		}
	}
}

func (r *replicaHandle) awaitExists(ctx context.Context) error {
	r.existsOnce.Do(func() {
		r.existsErr = r.supervisor.AwaitExists(ctx)
	})
	return r.existsErr
}

func normalizeBlueGreenSwitch(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return stack.BlueGreenSwitchPorts, nil
	}

	canonical := strings.ToLower(strings.ReplaceAll(trimmed, "-", ""))
	switch canonical {
	case stack.BlueGreenSwitchPorts:
		return stack.BlueGreenSwitchPorts, nil
	case strings.ToLower(stack.BlueGreenSwitchProxyLabel):
		return stack.BlueGreenSwitchProxyLabel, nil
	default:
		return "", fmt.Errorf("unsupported value %q (supported values: %s, %s)", trimmed, stack.BlueGreenSwitchPorts, stack.BlueGreenSwitchProxyLabel)
	}
}

func (r *replicaHandle) awaitStarted(ctx context.Context) error {
	r.startedOnce.Do(func() {
		r.startedErr = r.supervisor.AwaitStarted(ctx)
	})
	return r.startedErr
}

func (r *replicaHandle) awaitReady(ctx context.Context) error {
	r.readyOnce.Do(func() {
		r.readyErr = r.supervisor.AwaitReady(ctx)
	})
	return r.readyErr
}

func (h *serviceHandle) abortUpdate(updated []*replicaHandle, previous *stack.Service, cause error) error {
	return h.abortUpdateWithMessage(updated, previous, "promotion aborted", cause)
}

func (h *serviceHandle) abortUpdateWithMessage(updated []*replicaHandle, previous *stack.Service, message string, cause error) error {
	if previous != nil && len(updated) > 0 {
		h.rollback(updated, previous)
	}
	replicaIdx := -1
	if len(updated) > 0 && updated[0] != nil {
		replicaIdx = updated[0].index
	}
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		trimmed = "promotion aborted"
	}
	sendEvent(h.events, h.name, replicaIdx, EventTypeAborted, trimmed, 0, ReasonAborted, cause)
	return fmt.Errorf("service %s promotion failed: %w", h.name, cause)
}

func abortAfterFailures(spec *stack.Service) int {
	if spec == nil || spec.Update == nil {
		return 0
	}
	return spec.Update.AbortAfterFailures
}

func observationWindowDuration(spec *stack.Service) time.Duration {
	if spec == nil || spec.Update == nil {
		return 0
	}
	return spec.Update.ObservationWindow.Duration
}

func monitorResult(m *updateFailureMonitor, err error) (string, error, bool) {
	if m == nil {
		return "", nil, false
	}
	if msg, cause, triggered := m.Result(); triggered {
		return msg, cause, true
	}
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		if msg, cause, triggered := m.Result(); triggered {
			return msg, cause, true
		}
	}
	return "", nil, false
}

type updateFailureMonitor struct {
	ctx       context.Context
	cancel    context.CancelFunc
	propagate context.CancelFunc
	window    time.Duration
	thresh    int

	mu        sync.Mutex
	failures  []time.Time
	message   string
	cause     error
	triggered bool

	wg sync.WaitGroup
}

func newUpdateFailureMonitor(ctx context.Context, propagate context.CancelFunc, threshold int, window time.Duration) *updateFailureMonitor {
	if ctx == nil {
		ctx = context.Background()
	}
	monitorCtx, cancel := context.WithCancel(ctx)
	return &updateFailureMonitor{ctx: monitorCtx, cancel: cancel, propagate: propagate, window: window, thresh: threshold}
}

func (m *updateFailureMonitor) Track(rep *replicaHandle) {
	if m == nil || rep == nil || rep.supervisor == nil {
		return
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for {
			err := rep.awaitFailure(m.ctx)
			if err == nil {
				continue
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				if m.ctx.Err() != nil {
					return
				}
			}
			if m.recordFailure(rep.index, err) {
				return
			}
		}
	}()
}

func (m *updateFailureMonitor) recordFailure(replica int, failure error) bool {
	now := time.Now()
	m.mu.Lock()
	if m.triggered {
		m.mu.Unlock()
		return true
	}
	if m.window > 0 && len(m.failures) > 0 {
		cutoff := now.Add(-m.window)
		kept := m.failures[:0]
		for _, ts := range m.failures {
			if ts.After(cutoff) {
				kept = append(kept, ts)
			}
		}
		m.failures = kept
	}
	m.failures = append(m.failures, now)
	reached := len(m.failures) >= m.thresh
	if reached {
		if m.window > 0 {
			m.message = fmt.Sprintf("promotion aborted after %d readiness failures within %s", m.thresh, m.window)
		} else {
			m.message = fmt.Sprintf("promotion aborted after %d readiness failures", m.thresh)
		}
		m.cause = fmt.Errorf("%s (last failure from replica %d: %w)", m.message, replica, failure)
		m.triggered = true
	}
	m.mu.Unlock()
	if reached {
		m.cancel()
		if m.propagate != nil {
			m.propagate()
		}
	}
	return reached
}

func (m *updateFailureMonitor) Result() (string, error, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.triggered || m.cause == nil {
		return "", nil, false
	}
	return m.message, m.cause, true
}

func (m *updateFailureMonitor) Stop() {
	if m == nil {
		return
	}
	m.cancel()
	m.wg.Wait()
}

func (h *serviceHandle) rollback(replicas []*replicaHandle, previous *stack.Service) {
	if previous == nil {
		return
	}
	for _, replica := range replicas {
		if replica == nil || replica.supervisor == nil {
			continue
		}
		rollbackCtx, cancel := context.WithTimeout(context.Background(), promotionRollbackTimeout)
		replica.supervisor.UpdateServiceSpec(previous)
		_ = replica.supervisor.Restart(rollbackCtx)
		cancel()
	}
}

func promoteAfterDuration(spec *stack.Service) time.Duration {
	if spec == nil || spec.Update == nil {
		return 0
	}
	if spec.Update.PromoteAfter.Duration <= 0 {
		return 0
	}
	return spec.Update.PromoteAfter.Duration
}

func (h *serviceHandle) installController(ctrl *updateController) error {
	h.controllerMu.Lock()
	defer h.controllerMu.Unlock()
	if h.controller != nil {
		return fmt.Errorf("service %s update already in progress", h.name)
	}
	h.controller = ctrl
	return nil
}

func (h *serviceHandle) clearController(ctrl *updateController) {
	h.controllerMu.Lock()
	if h.controller == ctrl {
		h.controller = nil
	}
	h.controllerMu.Unlock()
}

func (h *serviceHandle) promote(ctx context.Context) error {
	if h == nil {
		return fmt.Errorf("service handle is nil")
	}
	h.controllerMu.Lock()
	controller := h.controller
	h.controllerMu.Unlock()
	if controller == nil {
		return fmt.Errorf("service %s has no pending promotion: %w", h.name, ErrNoPromotionPending)
	}
	return controller.promote(ctx)
}

type updateController struct {
	promoteCh   chan struct{}
	done        chan struct{}
	triggerOnce sync.Once
	finishOnce  sync.Once

	mu  sync.Mutex
	err error
}

func newUpdateController() *updateController {
	return &updateController{
		promoteCh: make(chan struct{}),
		done:      make(chan struct{}),
	}
}

func (c *updateController) startAutoPromote(d time.Duration) {
	go func() {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
			c.trigger()
		case <-c.promoteCh:
		case <-c.done:
		}
	}()
}

func (c *updateController) wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.promoteCh:
		return nil
	case <-c.done:
		return c.Err()
	}
}

func (c *updateController) trigger() {
	c.triggerOnce.Do(func() {
		close(c.promoteCh)
	})
}

func (c *updateController) promote(ctx context.Context) error {
	c.trigger()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-c.done:
		return c.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *updateController) finish(err error) {
	c.finishOnce.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
	})
}

func (c *updateController) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Up launches services described by the stack in topological order. Events are
// delivered to the supplied channel. The returned deployment must be stopped by
// the caller to release resources.
func (o *Orchestrator) Up(ctx context.Context, doc *stack.StackFile, graph *Graph, events chan<- Event) (*Deployment, error) {
	if doc == nil {
		return nil, errors.New("stack document is nil")
	}
	if graph == nil {
		return nil, errors.New("dependency graph is nil")
	}

	services := graph.Services()
	if len(services) == 0 {
		return &Deployment{handles: nil}, nil
	}

	handles := make(map[string]*serviceHandle, len(services))
	proxyConfig := graph.ProxySpec()

	for _, name := range services {
		svc := graph.ServiceSpec(name)
		if svc == nil {
			return nil, fmt.Errorf("service %s missing from stack", name)
		}

		runtimeImpl, ok := o.runtimes[svc.Runtime]
		if !ok {
			return nil, fmt.Errorf("service %s references unsupported runtime %q", name, svc.Runtime)
		}

		replicaCount := svc.Replicas
		if replicaCount < 1 {
			replicaCount = 1
		}

		svcClone := svc.Clone()
		if svcClone == nil {
			svcClone = &stack.Service{}
		}
		if name == stackProxyServiceName {
			svcClone.Runtime = proxyruntime.RuntimeName
			svcClone.ResolvedWorkdir = doc.Stack.Workdir
		}

		replicas := make([]*replicaHandle, 0, replicaCount)
		for idx := 0; idx < replicaCount; idx++ {
			var supProxy *stack.Proxy
			if name == stackProxyServiceName && proxyConfig != nil {
				supProxy = proxyConfig.Clone()
			}
			sup := newSupervisor(name, idx, svcClone, runtimeImpl, events, supProxy)
			replicas = append(replicas, &replicaHandle{index: idx, supervisor: sup})
		}

		handles[name] = &serviceHandle{name: name, service: svcClone, replicas: replicas, events: events}
	}

	deployment := &Deployment{handles: make([]*serviceHandle, 0, len(services))}

	for i := len(services) - 1; i >= 0; i-- {
		name := services[i]
		handle := handles[name]
		svc := graph.ServiceSpec(name)

		for _, dep := range svc.DependsOn {
			depHandle, ok := handles[dep.Target]
			if !ok {
				return nil, fmt.Errorf("service %s references unknown dependency %q", name, dep.Target)
			}

			require := dep.Require
			if require == "" {
				require = "ready"
			}

			waitCtx := ctx
			var cancel context.CancelFunc
			if dep.Timeout.Duration > 0 {
				waitCtx, cancel = context.WithTimeout(ctx, dep.Timeout.Duration)
			}

			var err error
			switch require {
			case "ready":
				err = depHandle.awaitReady(waitCtx)
			case "started":
				err = depHandle.awaitStarted(waitCtx)
			case "exists":
				err = depHandle.awaitExists(waitCtx)
			default:
				err = fmt.Errorf("unknown require value %q", require)
			}
			if cancel != nil {
				cancel()
			}
			if err != nil {
				blockErr := fmt.Errorf("service %s blocked waiting for %s (%s): %w", name, dep.Target, require, err)
				sendEvent(
					events,
					name,
					-1,
					EventTypeBlocked,
					fmt.Sprintf("blocked waiting for %s (%s)", dep.Target, require),
					0,
					fmt.Sprintf("%s: %v", ReasonDependencyBlocked, blockErr),
					blockErr,
				)
				if cleanupErr := cleanupDeployment(deployment, events); cleanupErr != nil {
					blockErr = fmt.Errorf("%w (cleanup failed: %v)", blockErr, cleanupErr)
				}
				return nil, blockErr
			}
		}

		handle.start(ctx)
		deployment.handles = append(deployment.handles, handle)
	}

	for _, handle := range deployment.handles {
		if err := handle.awaitReady(ctx); err != nil {
			readyErr := fmt.Errorf("service %s failed readiness: %w", handle.name, err)
			if cleanupErr := cleanupDeployment(deployment, events); cleanupErr != nil {
				readyErr = fmt.Errorf("%w (cleanup failed: %v)", readyErr, cleanupErr)
			}
			return nil, readyErr
		}
	}

	return deployment, nil
}

// Stop terminates all services tracked by the deployment in reverse order. The
// method is idempotent; subsequent calls return the first error that occurred.
func (d *Deployment) Stop(ctx context.Context, events chan<- Event) error {
	d.stopOnce.Do(func() {
		var firstErr error
		for i := len(d.handles) - 1; i >= 0; i-- {
			handle := d.handles[i]
			if err := handle.stop(ctx, events); err != nil {
				if firstErr == nil {
					firstErr = err
				}
			}
		}
		d.stopErr = firstErr
	})
	return d.stopErr
}

func cleanupDeployment(dep *Deployment, events chan<- Event) error {
	if dep == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return dep.Stop(ctx, events)
}
