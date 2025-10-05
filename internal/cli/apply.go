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

			if err := applyUpdates(cmd, ctx, doc.File.Stack.Name, dep, oldSpec, newSpec, targets); err != nil {
				return err
			}
			return nil
		},
	}
	return cmd
}

func applyUpdates(cmd *cobra.Command, cliCtx *context, stackName string, dep *engine.Deployment, oldSpec, newSpec map[string]*stack.Service, services []string) error {
	tracker := cliCtx.statusTracker()
	currentSpec := stack.CloneServiceMap(oldSpec)
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

		strategy := "rolling"
		if svcSpec.Update != nil && svcSpec.Update.Strategy != "" {
			strategy = svcSpec.Update.Strategy
		}
		autoPromote := strategy != "canary"

		printCanaryPrompt := func(detail string) {
			detail = strings.TrimSpace(detail)
			if detail != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Service %s canary ready (%s); run `orco promote %s` to continue rollout.\n", name, detail, name)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Service %s canary ready; run `orco promote %s` to continue rollout.\n", name, name)
			}
		}

		updateStart := time.Now()
		errCh := make(chan error, 1)
		go func() {
			errCh <- runServiceUpdate(cmd.Context(), service, svcSpec, autoPromote)
		}()

		ticker := time.NewTicker(50 * time.Millisecond)
		var updateErr error
		printedCanary := false

	updateLoop:
		for {
			select {
			case updateErr = <-errCh:
				ticker.Stop()
				if updateErr != nil {
					rollbackErr := rollbackUpdates(cmd.Context(), dep, oldSpec, updated)
					cliCtx.setDeployment(dep, stackName, oldSpec)
					if rollbackErr != nil {
						return fmt.Errorf("update %s failed: %v (rollback error: %v)", name, updateErr, rollbackErr)
					}
					return fmt.Errorf("update %s failed: %w", name, updateErr)
				}
				break updateLoop
			case <-ticker.C:
				if !autoPromote && !printedCanary {
					snapshot := tracker.Snapshot()
					if status, ok := snapshot[name]; ok && status.State == engine.EventTypeCanary && status.LastEvent.After(updateStart) {
						printCanaryPrompt(status.Message)
						printedCanary = true
						continue
					}
					history := tracker.History(name, 5)
					for _, transition := range history {
						if transition.Type == engine.EventTypeCanary && transition.Timestamp.After(updateStart) {
							printCanaryPrompt(transition.Message)
							printedCanary = true
							break
						}
					}
				}
			case <-cmd.Context().Done():
				ticker.Stop()
				return cmd.Context().Err()
			}
		}

		if !autoPromote && !printedCanary {
			history := tracker.History(name, 5)
			for _, transition := range history {
				if transition.Type == engine.EventTypeCanary && transition.Timestamp.After(updateStart) {
					printCanaryPrompt(transition.Message)
					printedCanary = true
					break
				}
			}
		}

		currentSpec[name] = svcSpec.Clone()
		cliCtx.setDeployment(dep, stackName, currentSpec)

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
		if err := runServiceUpdate(ctx, service, spec, true); err != nil {
			errs = append(errs, fmt.Errorf("rollback %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func runServiceUpdate(ctx stdcontext.Context, service *engine.Service, spec *stack.Service, autoPromote bool) error {
	if ctx == nil {
		ctx = stdcontext.Background()
	}

	if !autoPromote {
		return service.Update(ctx, spec)
	}

	updateErrCh := make(chan error, 1)
	updateDone := make(chan struct{})
	go func() {
		updateErrCh <- service.Update(ctx, spec)
		close(updateDone)
	}()

	var promoteErr error

promoteLoop:
	for {
		promoteErr = service.Promote(ctx)
		if !errors.Is(promoteErr, engine.ErrNoPromotionPending) {
			break
		}
		select {
		case <-updateDone:
			promoteErr = nil
			break promoteLoop
		case <-ctx.Done():
			promoteErr = ctx.Err()
			break promoteLoop
		case <-time.After(10 * time.Millisecond):
		}
	}

	updateErr := <-updateErrCh
	if updateErr != nil {
		return updateErr
	}
	return promoteErr
}
