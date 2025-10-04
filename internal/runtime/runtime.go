package runtime

import (
	"context"
	"time"

	"github.com/example/orco/internal/probe"
	"github.com/example/orco/internal/stack"
)

// Instance represents a single running service instance managed by a runtime
// adapter.
type Instance interface {
	// WaitReady blocks until the instance is considered ready or the
	// provided context is cancelled.
	WaitReady(ctx context.Context) error

	// Wait blocks until the instance exits or the provided context is
	// cancelled. Implementations should return any error associated with
	// the termination of the underlying process/container. A nil error
	// indicates a clean shutdown.
	Wait(ctx context.Context) error

	// Health returns a channel that delivers readiness transitions for the
	// instance. A nil channel indicates that the runtime does not surface
	// health information beyond the initial readiness gate.
	Health() <-chan probe.State

	// Stop terminates the instance. Implementations should be idempotent
	// and safe to call multiple times.
	Stop(ctx context.Context) error

	// Logs returns a channel of log entries associated with the instance.
	// The channel should be closed once the instance has stopped. A nil
	// channel indicates that the runtime does not provide log streaming.
	Logs() <-chan LogEntry
}

// Runtime describes a backend capable of launching services.
type Runtime interface {
	// Start launches the provided service and returns a handle to the
	// running instance. Implementations should respect context cancellation
	// and surface failures via returned errors.
	Start(ctx context.Context, name string, svc *stack.Service) (Instance, error)
}

// LogEntry represents a single line of log output emitted by an instance.
type LogEntry struct {
	// Message contains the raw log payload.
	Message string

	// Source identifies the origin of the message, such as stdout, stderr,
	// or an internal Orco component.
	Source string

	// Level expresses the semantic severity associated with the entry.
	// Implementations may leave this empty to request default normalization.
	Level string

	// Timestamp captures the emission time as observed by the runtime. If
	// zero, higher layers will substitute their own timestamp during
	// normalization.
	Timestamp time.Time
}

const (
	// LogSourceStdout indicates the entry originated from standard output.
	LogSourceStdout = "stdout"

	// LogSourceStderr indicates the entry originated from standard error.
	LogSourceStderr = "stderr"

	// LogSourceSystem identifies log lines synthesized by Orco itself.
	LogSourceSystem = "orco"
)

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
