package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Paintersrp/orco/internal/runtime"
	"github.com/Paintersrp/orco/internal/stack"
)

// Orchestrator coordinates runtime adapters to bring services up respecting the
// dependency DAG.
type Orchestrator struct {
	runtimes runtime.Registry
}

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

type serviceHandle struct {
	name     string
	replicas []*replicaHandle
}

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

func (r *replicaHandle) awaitExists(ctx context.Context) error {
	r.existsOnce.Do(func() {
		r.existsErr = r.supervisor.AwaitExists(ctx)
	})
	return r.existsErr
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
	for _, name := range services {
		svc, ok := doc.Services[name]
		if !ok {
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

		replicas := make([]*replicaHandle, 0, replicaCount)
		for idx := 0; idx < replicaCount; idx++ {
			sup := newSupervisor(name, idx, svc, runtimeImpl, events)
			replicas = append(replicas, &replicaHandle{index: idx, supervisor: sup})
		}

		handles[name] = &serviceHandle{name: name, replicas: replicas}
	}

	deployment := &Deployment{handles: make([]*serviceHandle, 0, len(services))}

	for i := len(services) - 1; i >= 0; i-- {
		name := services[i]
		handle := handles[name]
		svc := doc.Services[name]

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
