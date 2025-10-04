package engine

import (
	"time"

	"github.com/Paintersrp/orco/internal/runtime"
)

// EventType captures high level lifecycle notifications emitted by supervisors
// and the orchestrator.
type EventType string

const (
	EventTypeStarting EventType = "starting"
	EventTypeReady    EventType = "ready"
	EventTypeStopping EventType = "stopping"
	EventTypeStopped  EventType = "stopped"
	EventTypeLog      EventType = "log"
	EventTypeError    EventType = "error"
	EventTypeUnready  EventType = "unready"
	EventTypeCrashed  EventType = "crashed"
	EventTypeFailed   EventType = "failed"
)

// Event represents a single lifecycle or log notification.
type Event struct {
	Timestamp time.Time
	Service   string
	Replica   int
	Type      EventType
	Message   string
	Level     string
	Source    string
	Err       error
	Attempt   int
	Reason    string
}

const (
	ReasonInitialStart   = "initial_start"
	ReasonRestart        = "restart"
	ReasonStartFailure   = "start_failure"
	ReasonInstanceCrash  = "instance_crash"
	ReasonRetriesExhaust = "retries_exhausted"
	ReasonLogStreamError = "log_stream_error"
	ReasonProbeReady     = "probe_ready"
	ReasonProbeUnready   = "probe_unready"
	ReasonSupervisorStop = "supervisor_stop"
	ReasonStopFailed     = "stop_failed"
	ReasonShutdown       = "shutdown"
)

func sendEvent(events chan<- Event, service string, t EventType, message string, attempt int, reason string, err error) {
	if events == nil {
		return
	}
	events <- Event{
		Timestamp: time.Now(),
		Service:   service,
		Replica:   0,
		Type:      t,
		Message:   message,
		Level:     "info",
		Source:    runtime.LogSourceSystem,
		Err:       err,
		Attempt:   attempt,
		Reason:    reason,
	}
}
