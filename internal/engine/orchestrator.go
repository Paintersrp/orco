package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/example/orco/internal/runtime"
	"github.com/example/orco/internal/stack"
)

// EventType captures high level lifecycle notifications emitted by the
// orchestrator.
type EventType string

const (
	EventTypeStarting EventType = "starting"
	EventTypeStarted  EventType = "started"
	EventTypeReady    EventType = "ready"
	EventTypeStopping EventType = "stopping"
	EventTypeStopped  EventType = "stopped"
	EventTypeLog      EventType = "log"
	EventTypeError    EventType = "error"
)

// Event represents a single lifecycle or log notification.
type Event struct {
	Timestamp time.Time
	Service   string
	Type      EventType
	Message   string
	Err       error
}

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
	logs    sync.WaitGroup

	stopOnce sync.Once
	stopErr  error
}

type serviceHandle struct {
	name     string
	instance runtime.Instance
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

	deployment := &Deployment{handles: make([]*serviceHandle, 0, len(graph.Services()))}

	services := graph.Services()
	for i := len(services) - 1; i >= 0; i-- {
		name := services[i]
		svc, ok := doc.Services[name]
		if !ok {
			return nil, fmt.Errorf("service %s missing from stack", name)
		}
		runtimeImpl, ok := o.runtimes[svc.Runtime]
		if !ok {
			return nil, fmt.Errorf("service %s references unsupported runtime %q", name, svc.Runtime)
		}

		sendEvent(events, name, EventTypeStarting, "starting service", nil)
		instance, err := runtimeImpl.Start(ctx, name, svc)
		if err != nil {
			sendEvent(events, name, EventTypeError, "start failed", err)
			startErr := fmt.Errorf("start service %s: %w", name, err)
			if cleanupErr := cleanupDeployment(deployment, events); cleanupErr != nil {
				startErr = fmt.Errorf("%w (cleanup failed: %v)", startErr, cleanupErr)
			}
			return nil, startErr
		}
		handle := &serviceHandle{name: name, instance: instance}
		deployment.handles = append(deployment.handles, handle)

		sendEvent(events, name, EventTypeStarted, "service started", nil)
		if logCh := instance.Logs(); logCh != nil {
			deployment.logs.Add(1)
			go streamLogs(name, logCh, events, &deployment.logs)
		}

		if err := instance.WaitReady(ctx); err != nil {
			sendEvent(events, name, EventTypeError, "readiness failed", err)
			readyErr := fmt.Errorf("service %s failed readiness: %w", name, err)
			if cleanupErr := cleanupDeployment(deployment, events); cleanupErr != nil {
				readyErr = fmt.Errorf("%w (cleanup failed: %v)", readyErr, cleanupErr)
			}
			return nil, readyErr
		}
		sendEvent(events, name, EventTypeReady, "service ready", nil)
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
			if err := handle.instance.Stop(ctx); err != nil {
				sendEvent(events, handle.name, EventTypeError, "stop failed", err)
				if firstErr == nil {
					firstErr = fmt.Errorf("stop service %s: %w", handle.name, err)
				}
				continue
			}
			sendEvent(events, handle.name, EventTypeStopped, "service stopped", nil)
		}
		d.logs.Wait()
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

func sendEvent(events chan<- Event, service string, t EventType, message string, err error) {
	if events == nil {
		return
	}
	events <- Event{
		Timestamp: time.Now(),
		Service:   service,
		Type:      t,
		Message:   message,
		Err:       err,
	}
}

func streamLogs(service string, logs <-chan string, events chan<- Event, wg *sync.WaitGroup) {
	defer wg.Done()
	for line := range logs {
		sendEvent(events, service, EventTypeLog, line, nil)
	}
}
