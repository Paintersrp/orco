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
	name       string
	supervisor *supervisor

	existsOnce sync.Once
	existsErr  error

	startedOnce sync.Once
	startedErr  error

	readyOnce sync.Once
	readyErr  error
}

func (h *serviceHandle) awaitExists(ctx context.Context) error {
	h.existsOnce.Do(func() {
		h.existsErr = h.supervisor.AwaitExists(ctx)
	})
	return h.existsErr
}

func (h *serviceHandle) awaitStarted(ctx context.Context) error {
	h.startedOnce.Do(func() {
		h.startedErr = h.supervisor.AwaitStarted(ctx)
	})
	return h.startedErr
}

func (h *serviceHandle) awaitReady(ctx context.Context) error {
	h.readyOnce.Do(func() {
		h.readyErr = h.supervisor.AwaitReady(ctx)
	})
	return h.readyErr
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

		sup := newSupervisor(name, svc, runtimeImpl, events)
		handles[name] = &serviceHandle{name: name, supervisor: sup}
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
				if cleanupErr := cleanupDeployment(deployment, events); cleanupErr != nil {
					blockErr = fmt.Errorf("%w (cleanup failed: %v)", blockErr, cleanupErr)
				}
				return nil, blockErr
			}
		}

		handle.supervisor.Start(ctx)
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
			sendEvent(events, handle.name, EventTypeStopping, "stopping service", nil)
			if err := handle.supervisor.Stop(ctx); err != nil {
				sendEvent(events, handle.name, EventTypeError, "stop failed", err)
				if firstErr == nil {
					firstErr = fmt.Errorf("stop service %s: %w", handle.name, err)
				}
				continue
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
