package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func newRestartCmd(ctx *context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restart [service]",
		Short: "Plan a rolling restart of the specified service",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := ctx.loadStack()
			if err != nil {
				return err
			}
			svcName := args[0]
			svc, ok := doc.File.Services[svcName]
			if !ok {
				return fmt.Errorf("unknown service %s", svcName)
			}
			strategy := "rolling"
			if svc.Update != nil && svc.Update.Strategy != "" {
				strategy = svc.Update.Strategy
			}
			if strategy != "rolling" && strategy != "canary" {
				return fmt.Errorf("unsupported update strategy %q for service %s", strategy, svcName)
			}

			dep, stackName := ctx.currentDeploymentInfo()
			if dep == nil {
				return errors.New("no active deployment to restart")
			}
			if stackName != "" && stackName != doc.File.Stack.Name {
				return fmt.Errorf("active deployment %s does not match stack %s", stackName, doc.File.Stack.Name)
			}

			service, ok := dep.Service(svcName)
			if !ok {
				return fmt.Errorf("service %s is not currently running", svcName)
			}

			replicas := service.Replicas()
			if replicas < 1 {
				replicas = 1
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Rolling restart of %s (%d replicas)\n", svcName, replicas)

			tracker := ctx.statusTracker()

			for i := 0; i < replicas; i++ {
				fmt.Fprintf(cmd.OutOrStdout(), "Restarting %s replica %d/%d...\n", svcName, i+1, replicas)
				if err := service.RestartReplica(cmd.Context(), i); err != nil {
					return fmt.Errorf("restart %s replica %d: %w", svcName, i, err)
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
						return fmt.Errorf("timed out waiting for %s readiness after restarting replica %d", svcName, i)
					}
					select {
					case <-cmd.Context().Done():
						return cmd.Context().Err()
					case <-time.After(50 * time.Millisecond):
					}
				}

				readyReplicas := status.ReadyReplicas
				if readyReplicas > replicas {
					readyReplicas = replicas
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Replica %d/%d ready (service readiness: %d/%d, Ready)\n", i+1, replicas, readyReplicas, replicas)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Completed rolling restart of %s.\n", svcName)
			return nil
		},
	}
	return cmd
}
