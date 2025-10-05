package cli

import (
	stdcontext "context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Paintersrp/orco/internal/engine"
	"github.com/Paintersrp/orco/internal/stack"
)

func newApplyCmd(ctx *context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Reconcile stack changes",
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := ctx.loadStack()
			if err != nil {
				return err
			}

			dep, stackName := ctx.currentDeploymentInfo()
			if dep == nil {
				return errors.New("no active deployment to apply changes to")
			}
			if stackName != "" && stackName != doc.File.Stack.Name {
				return fmt.Errorf("active deployment %s does not match stack %s", stackName, doc.File.Stack.Name)
			}

			oldSpec := ctx.currentDeploymentSpec()
			if len(oldSpec) == 0 {
				return errors.New("no stored stack specification for active deployment")
			}

			newSpec := stack.CloneServiceMap(doc.File.Services)
			diffs, targets, unsupported := diffStackServices(oldSpec, newSpec)

			diffOutput := formatServiceDiffs(diffs)
			fmt.Fprintln(cmd.OutOrStdout(), diffOutput)
			if len(diffs) == 0 {
				return nil
			}

			if len(unsupported) > 0 {
				return fmt.Errorf("apply does not support adding or removing services: %s", strings.Join(unsupported, ", "))
			}

			if err := applyUpdates(cmd, ctx, dep, oldSpec, newSpec, targets); err != nil {
				return err
			}

			ctx.setDeployment(dep, doc.File.Stack.Name, newSpec)
			return nil
		},
	}
	return cmd
}

func applyUpdates(cmd *cobra.Command, cliCtx *context, dep *engine.Deployment, oldSpec, newSpec map[string]*stack.Service, services []string) error {
	tracker := cliCtx.statusTracker()
	updated := make([]string, 0, len(services))

	for _, name := range services {
		svcSpec := newSpec[name]
		service, ok := dep.Service(name)
		if !ok {
			return fmt.Errorf("service %s is not currently running", name)
		}

		replicas := service.Replicas()
		if replicas < 1 {
			replicas = 1
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Updating %s (%d replicas)\n", name, replicas)
		updated = append(updated, name)
		if err := service.Update(cmd.Context(), svcSpec); err != nil {
			rollbackErr := rollbackUpdates(cmd.Context(), dep, oldSpec, updated)
			if rollbackErr != nil {
				return fmt.Errorf("update %s failed: %v (rollback error: %v)", name, err, rollbackErr)
			}
			return fmt.Errorf("update %s failed: %w", name, err)
		}

		status := tracker.Snapshot()[name]
		if status.ReadyReplicas < replicas {
			deadline := time.Now().Add(5 * time.Second)
			for status.ReadyReplicas < replicas && time.Now().Before(deadline) {
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(50 * time.Millisecond):
				}
				status = tracker.Snapshot()[name]
			}
		}
		ready := status.ReadyReplicas
		if ready > replicas {
			ready = replicas
		}
		state := "Not Ready"
		if status.Ready {
			state = "Ready"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Service %s ready (%d/%d replicas, %s)\n", name, ready, replicas, state)
	}

	return nil
}

func rollbackUpdates(ctx stdcontext.Context, dep *engine.Deployment, oldSpec map[string]*stack.Service, services []string) error {
	var errs []error
	for i := len(services) - 1; i >= 0; i-- {
		name := services[i]
		spec := oldSpec[name]
		if spec == nil {
			continue
		}
		service, ok := dep.Service(name)
		if !ok {
			errs = append(errs, fmt.Errorf("rollback %s: service no longer running", name))
			continue
		}
		if err := service.Update(ctx, spec); err != nil {
			errs = append(errs, fmt.Errorf("rollback %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}
