package cli

import (
	stdcontext "context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Paintersrp/orco/internal/api"
)

func newRestartCmd(ctx *context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restart [service]",
		Short: "Plan a rolling restart of the specified service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := restartService(cmd.Context(), ctx, args[0], cmd.OutOrStdout())
			return err
		},
	}
	return cmd
}

func restartService(ctx stdcontext.Context, cliCtx *context, svcName string, out io.Writer) (*api.RestartResult, error) {
	doc, err := cliCtx.loadStack()
	if err != nil {
		return nil, err
	}

	svc, ok := doc.File.Services[svcName]
	if !ok {
		return nil, fmt.Errorf("%w %s", api.ErrUnknownService, svcName)
	}
	strategy := "rolling"
	if svc.Update != nil && strings.TrimSpace(svc.Update.Strategy) != "" {
		strategy = strings.ToLower(strings.TrimSpace(svc.Update.Strategy))
	}
	if strategy != "rolling" && strategy != "canary" && strategy != "bluegreen" {
		return nil, fmt.Errorf("%w: unsupported update strategy %q for service %s", api.ErrUnsupportedChange, strategy, svcName)
	}

	dep, stackName := cliCtx.currentDeploymentInfo()
	if dep == nil {
		return nil, fmt.Errorf("%w to restart", api.ErrNoActiveDeployment)
	}
	if stackName != "" && stackName != doc.File.Stack.Name {
		return nil, fmt.Errorf("%w: active deployment %s does not match stack %s", api.ErrStackMismatch, stackName, doc.File.Stack.Name)
	}

	service, ok := dep.Service(svcName)
	if !ok {
		return nil, fmt.Errorf("%w: service %s is not currently running", api.ErrServiceNotRunning, svcName)
	}

	replicas := service.Replicas()
	if replicas < 1 {
		replicas = 1
	}

	if out != nil {
		fmt.Fprintf(out, "Rolling restart of %s (%d replicas)\n", svcName, replicas)
	}

	tracker := cliCtx.statusTracker()

	for i := 0; i < replicas; i++ {
		if out != nil {
			fmt.Fprintf(out, "Restarting %s replica %d/%d...\n", svcName, i+1, replicas)
		}
		if err := service.RestartReplica(ctx, i); err != nil {
			return nil, fmt.Errorf("restart %s replica %d: %w", svcName, i, err)
		}

		var status ServiceStatus
		deadline := time.Now().Add(5 * time.Second)
		for {
			snapshot := tracker.Snapshot()
			status = snapshot[svcName]
			if status.Ready && status.ReadyReplicas >= replicas {
				break
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("%w waiting for %s readiness after restarting replica %d", api.ErrReadinessTimeout, svcName, i)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}

		readyReplicas := status.ReadyReplicas
		if readyReplicas > replicas {
			readyReplicas = replicas
		}
		if out != nil {
			fmt.Fprintf(out, "Replica %d/%d ready (service readiness: %d/%d, Ready)\n", i+1, replicas, readyReplicas, replicas)
		}
	}

	if out != nil {
		fmt.Fprintf(out, "Completed rolling restart of %s.\n", svcName)
	}

	finalStatus := tracker.Snapshot()[svcName]
	result := &api.RestartResult{
		Service:       svcName,
		Replicas:      replicas,
		ReadyReplicas: min(finalStatus.ReadyReplicas, replicas),
		CompletedAt:   time.Now(),
		Restarts:      finalStatus.Restarts,
	}
	return result, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
