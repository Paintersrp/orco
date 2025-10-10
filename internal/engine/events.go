package engine

import (
	"time"

	"github.com/Paintersrp/orco/internal/runtime"
)

// EventType captures high level lifecycle notifications emitted by supervisors
// and the orchestrator.
type EventType string

const (
	EventTypeStarting    EventType = "starting"
	EventTypeReady       EventType = "ready"
	EventTypeStopping    EventType = "stopping"
	EventTypeStopped     EventType = "stopped"
	EventTypeLog         EventType = "log"
	EventTypeError       EventType = "error"
	EventTypeUnready     EventType = "unready"
	EventTypeCrashed     EventType = "crashed"
	EventTypeFailed      EventType = "failed"
	EventTypeBlocked     EventType = "blocked"
	EventTypeCanary      EventType = "canary"
	EventTypePromoted    EventType = "promoted"
	EventTypeAborted     EventType = "aborted"
	EventTypeUpdatePhase EventType = "update_phase"
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
	ReasonInitialStart          = "initial_start"
	ReasonRestart               = "restart"
	ReasonStartFailure          = "start_failure"
	ReasonInstanceCrash         = "instance_crash"
	ReasonRetriesExhaust        = "retries_exhausted"
	ReasonLogStreamError        = "log_stream_error"
	ReasonProbeReady            = "probe_ready"
	ReasonProbeUnready          = "probe_unready"
	ReasonSupervisorStop        = "supervisor_stop"
	ReasonStopFailed            = "stop_failed"
	ReasonShutdown              = "shutdown"
	ReasonDependencyBlocked     = "dependency_blocked"
	ReasonCanary                = "canary"
	ReasonPromoted              = "promoted"
	ReasonAborted               = "aborted"
	ReasonBlueGreenProvision    = "bluegreen_provision"
	ReasonBlueGreenVerify       = "bluegreen_verify"
	ReasonBlueGreenCutover      = "bluegreen_cutover"
        ReasonBlueGreenDecommission = "bluegreen_decommission"
        ReasonBlueGreenRollback     = "bluegreen_rollback"
        ReasonHookFailed            = "hook_failed"
)

// BlueGreenPhase enumerates the major milestones emitted while executing a
// blue/green update. These map directly onto the event messages surfaced via
// EventTypeUpdatePhase notifications so that consumers can reason about the
// progress of the update.
type BlueGreenPhase string

const (
	// BlueGreenPhaseProvisionGreen indicates the controller is provisioning
	// the duplicate (green) replica set.
	BlueGreenPhaseProvisionGreen BlueGreenPhase = "ProvisionGreen"

	// BlueGreenPhaseVerify signals that the controller is waiting for the
	// newly provisioned replica set to become ready.
	BlueGreenPhaseVerify BlueGreenPhase = "Verify"

	// BlueGreenPhaseCutover denotes the point at which traffic is switched
	// from the blue replica set to the green replica set.
	BlueGreenPhaseCutover BlueGreenPhase = "Cutover"

	// BlueGreenPhaseDecommission communicates that the old (blue) replica
	// set is being shut down following a successful cutover.
	BlueGreenPhaseDecommission BlueGreenPhase = "DecommissionBlue"
)

// blueGreenPhaseMessage normalises the event message attached to blue/green
// phase notifications.
func blueGreenPhaseMessage(phase BlueGreenPhase) string {
	if phase == "" {
		return ""
	}
	return string(phase)
}

func sendEvent(events chan<- Event, service string, replica int, t EventType, message string, attempt int, reason string, err error) {
	if events == nil {
		return
	}
	events <- Event{
		Timestamp: time.Now(),
		Service:   service,
		Replica:   replica,
		Type:      t,
		Message:   message,
		Level:     "info",
		Source:    runtime.LogSourceSystem,
		Err:       err,
		Attempt:   attempt,
		Reason:    reason,
	}
}
