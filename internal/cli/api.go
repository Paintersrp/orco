package cli

import (
	stdcontext "context"
	"fmt"
	"time"

	"github.com/Paintersrp/orco/internal/api"
)

const defaultHistoryDepth = 10

// ControlAPI exposes orchestrator operations for the HTTP control plane.
type ControlAPI struct {
	ctx *context
}

// NewControlAPI constructs a ControlAPI wrapper around the shared CLI context.
func NewControlAPI(ctx *context) *ControlAPI {
	if ctx == nil {
		return nil
	}
	return &ControlAPI{ctx: ctx}
}

// Status returns the current orchestrator status snapshot.
func (apiCtrl *ControlAPI) Status(ctx stdcontext.Context) (*api.StatusReport, error) {
	if apiCtrl == nil || apiCtrl.ctx == nil {
		return nil, fmt.Errorf("%w", api.ErrNoActiveDeployment)
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	if apiCtrl.ctx.currentDeployment() == nil {
		return nil, fmt.Errorf("%w for status", api.ErrNoActiveDeployment)
	}
	tracker := apiCtrl.ctx.statusTracker()
	snapshot := tracker.Snapshot()
	services := make(map[string]api.ServiceReport, len(snapshot))
	for name, status := range snapshot {
		history := tracker.History(name, defaultHistoryDepth)
		apiHistory := make([]api.ServiceTransition, 0, len(history))
		for _, entry := range history {
			apiHistory = append(apiHistory, api.ServiceTransition{
				Timestamp: entry.Timestamp,
				Type:      entry.Type,
				Reason:    entry.Reason,
				Message:   entry.Message,
			})
		}
		lastReason := ""
		if len(apiHistory) > 0 {
			lastReason = apiHistory[len(apiHistory)-1].Reason
		}
		services[name] = api.ServiceReport{
			Name:          status.Name,
			State:         status.State,
			Ready:         status.Ready,
			Restarts:      status.Restarts,
			Replicas:      status.Replicas,
			ReadyReplicas: status.ReadyReplicas,
			Message:       status.Message,
			FirstSeen:     status.FirstSeen,
			LastEvent:     status.LastEvent,
			Resources: api.ResourceHint{
				CPU:    status.Resources.CPU,
				Memory: status.Resources.Memory,
			},
			History:    apiHistory,
			LastReason: lastReason,
		}
	}

	_, stackName := apiCtrl.ctx.currentDeploymentInfo()
	doc, err := apiCtrl.ctx.loadStack()
	var version string
	if err == nil && doc != nil && doc.File != nil {
		if stackName == "" {
			stackName = doc.File.Stack.Name
		}
		version = doc.File.Version
	}

	report := &api.StatusReport{
		Stack:       stackName,
		Version:     version,
		GeneratedAt: time.Now(),
		Services:    services,
	}
	return report, nil
}

// RestartService executes a rolling restart for the provided service using the orchestrator managed by the CLI context.
func (apiCtrl *ControlAPI) RestartService(ctx stdcontext.Context, service string) (*api.RestartResult, error) {
	return restartService(ctx, apiCtrl.ctx, service, nil)
}

// Apply reconciles stack changes using the orchestrator managed by the CLI context.
func (apiCtrl *ControlAPI) Apply(ctx stdcontext.Context) (*api.ApplyResult, error) {
	return runApply(ctx, apiCtrl.ctx, nil)
}

// Ensure interface compliance at compile time.
var _ api.Controller = (*ControlAPI)(nil)
