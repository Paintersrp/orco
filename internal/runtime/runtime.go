package runtime

import (
	"context"
	"time"

	"github.com/Paintersrp/orco/internal/probe"
	"github.com/Paintersrp/orco/internal/stack"
)

// Handle represents a single running service instance managed by a runtime
// adapter.
type Handle interface {
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

	// Kill forcefully terminates the instance without waiting for graceful
	// shutdown handlers. Implementations should best-effort deliver a hard
	// termination signal and return once the underlying process/container
	// has exited or the provided context is cancelled.
	Kill(ctx context.Context) error

	// Logs returns a channel of log entries associated with the instance.
	// The channel should be closed once the instance has stopped. A nil
	// channel indicates that the runtime does not provide log streaming.
	Logs(ctx context.Context) (<-chan LogEntry, error)
}

// Runtime describes a backend capable of launching services.
type Runtime interface {
	// Start launches the provided service and returns a handle to the
	// running instance. Implementations should respect context cancellation
	// and surface failures via returned errors.
	Start(ctx context.Context, spec StartSpec) (Handle, error)
}

// StartSpec captures the parameters required to launch a service instance via a
// runtime implementation.
type StartSpec struct {
	// Name is a human readable identifier for the instance.
	Name string

	// Image identifies the container image to launch (Docker runtime).
	Image string

	// Command describes the process invocation for the instance. The
	// runtime may interpret this either as an entrypoint override (Docker)
	// or an executable invocation (process runtime).
	Command []string

	// Env defines the environment variables that should be injected into
	// the launched instance.
	Env map[string]string

	// Ports enumerates the port mappings requested by the caller using the
	// Docker CLI notation (e.g. "127.0.0.1:8080:80/tcp").
	Ports []string

	// Volumes enumerates bind mounts requested for the instance. Each
	// volume represents a host path bound into the container at the
	// specified target. Modes map directly to the Docker bind options
	// string (e.g. "ro", "rw,cached").
	Volumes []Volume

	// Workdir configures the working directory for the launched process.
	Workdir string

	// Health optionally configures readiness probing for the instance.
	Health *stack.Health

	// Service retains the original stack definition when available. This is
	// primarily used by existing runtimes that need access to additional
	// configuration that has not yet been promoted onto StartSpec.
	Service *stack.Service
}

// Volume describes a bind mount exposed to the launched instance.
type Volume struct {
	// Source is the absolute host path for the bind mount.
	Source string

	// Target is the absolute container path where the source is mounted.
	Target string

	// Mode carries the bind options string passed to the runtime. An empty
	// value requests Docker's default behaviour (read-write).
	Mode string
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
