package runtime

import (
	"context"

	"github.com/example/orco/internal/stack"
)

// Instance represents a single running service instance managed by a runtime
// adapter.
type Instance interface {
	// WaitReady blocks until the instance is considered ready or the
	// provided context is cancelled.
	WaitReady(ctx context.Context) error

	// Stop terminates the instance. Implementations should be idempotent
	// and safe to call multiple times.
	Stop(ctx context.Context) error

	// Logs returns a channel of log lines associated with the instance. The
	// channel should be closed once the instance has stopped. A nil channel
	// indicates that the runtime does not provide log streaming.
	Logs() <-chan string
}

// Runtime describes a backend capable of launching services.
type Runtime interface {
	// Start launches the provided service and returns a handle to the
	// running instance. Implementations should respect context cancellation
	// and surface failures via returned errors.
	Start(ctx context.Context, name string, svc *stack.Service) (Instance, error)
}

// Registry maps runtime identifiers to their concrete implementations.
type Registry map[string]Runtime

// Clone returns a shallow copy of the registry, allowing callers to avoid
// accidental mutation of shared maps.
func (r Registry) Clone() Registry {
	dup := make(Registry, len(r))
	for k, v := range r {
		dup[k] = v
	}
	return dup
}
