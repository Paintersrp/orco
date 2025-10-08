package api

import (
	stdcontext "context"
	"errors"
	"time"

	"github.com/Paintersrp/orco/internal/engine"
)

var (
	ErrNoActiveDeployment = errors.New("no active deployment")
	ErrStackMismatch      = errors.New("stack mismatch")
	ErrUnknownService     = errors.New("unknown service")
	ErrServiceNotRunning  = errors.New("service not running")
	ErrNoStoredStackSpec  = errors.New("missing stack specification")
	ErrUnsupportedChange  = errors.New("unsupported change")
	ErrReadinessTimeout   = errors.New("service readiness timeout")
)

// ResourceHint captures formatted CPU and memory hints for API responses.
type ResourceHint struct {
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}

// ServiceTransition mirrors cli.ServiceTransition for API consumers.
type ServiceTransition struct {
	Timestamp time.Time        `json:"timestamp"`
	Type      engine.EventType `json:"type"`
	Reason    string           `json:"reason"`
	Message   string           `json:"message"`
}

// ServiceReport describes the runtime state for a single service.
type ServiceReport struct {
	Name          string              `json:"name"`
	State         engine.EventType    `json:"state"`
	Ready         bool                `json:"ready"`
	Restarts      int                 `json:"restarts"`
	Replicas      int                 `json:"replicas"`
	ReadyReplicas int                 `json:"ready_replicas"`
	Message       string              `json:"message"`
	FirstSeen     time.Time           `json:"first_seen"`
	LastEvent     time.Time           `json:"last_event"`
	Resources     ResourceHint        `json:"resources"`
	History       []ServiceTransition `json:"history"`
	LastReason    string              `json:"last_reason"`
}

// StatusReport aggregates stack-wide status information.
type StatusReport struct {
	Stack       string                   `json:"stack"`
	Version     string                   `json:"version"`
	GeneratedAt time.Time                `json:"generated_at"`
	Services    map[string]ServiceReport `json:"services"`
}

// RestartResult captures the outcome of a restart operation.
type RestartResult struct {
	Service       string    `json:"service"`
	Replicas      int       `json:"replicas"`
	ReadyReplicas int       `json:"ready_replicas"`
	CompletedAt   time.Time `json:"completed_at"`
	Restarts      int       `json:"restarts"`
}

// ApplyResult captures the outcome of an apply operation.
type ApplyResult struct {
	Diff     string                   `json:"diff"`
	Updated  []string                 `json:"updated"`
	Stack    string                   `json:"stack"`
	Snapshot map[string]ServiceReport `json:"snapshot"`
}

// Controller exposes orchestrator operations required by control servers.
type Controller interface {
	Status(stdcontext.Context) (*StatusReport, error)
	RestartService(stdcontext.Context, string) (*RestartResult, error)
	Apply(stdcontext.Context) (*ApplyResult, error)
}
