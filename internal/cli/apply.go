package cli

import (
	stdcontext "context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Paintersrp/orco/internal/api"
	"github.com/Paintersrp/orco/internal/engine"
	"github.com/Paintersrp/orco/internal/stack"
)

func newApplyCmd(ctx *context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Reconcile stack changes",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := runApply(cmd.Context(), ctx, cmd.OutOrStdout())
			return err
		},
	}
	return cmd
}

func runApply(ctx stdcontext.Context, cliCtx *context, out io.Writer) (*api.ApplyResult, error) {
	doc, err := cliCtx.loadStack()
	if err != nil {
		return nil, err
	}

	dep, stackName := cliCtx.currentDeploymentInfo()
	if dep == nil {
		return nil, fmt.Errorf("%w to apply changes to", api.ErrNoActiveDeployment)
	}
	if stackName != "" && stackName != doc.File.Stack.Name {
		return nil, fmt.Errorf("%w: active deployment %s does not match stack %s", api.ErrStackMismatch, stackName, doc.File.Stack.Name)
	}

	oldSpec := cliCtx.currentDeploymentSpec()
	if len(oldSpec) == 0 {
		return nil, fmt.Errorf("%w for active deployment", api.ErrNoStoredStackSpec)
	}

	newSpec := stack.CloneServiceMap(doc.File.Services)
	diffs, targets, unsupported := diffStackServices(oldSpec, newSpec)

	diffOutput := formatServiceDiffs(diffs)
	if out != nil {
		fmt.Fprintln(out, diffOutput)
	}

	result := &api.ApplyResult{
		Diff:    diffOutput,
		Updated: append([]string(nil), targets...),
		Stack:   doc.File.Stack.Name,
	}

	populateSnapshot := func() {
		control := NewControlAPI(cliCtx)
		if control == nil {
			return
		}
		if status, err := control.Status(ctx); err == nil && status != nil {
			result.Snapshot = status.Services
		}
	}

	if len(diffs) == 0 {
		populateSnapshot()
		return result, nil
	}

	if len(unsupported) > 0 {
		return nil, fmt.Errorf("%w: apply does not support adding or removing services (%s)", api.ErrUnsupportedChange, strings.Join(unsupported, ", "))
	}

	if err := applyUpdates(ctx, cliCtx, doc.File.Stack.Name, dep, oldSpec, newSpec, targets, out); err != nil {
		return nil, err
	}
	populateSnapshot()
	return result, nil
}

func applyUpdates(ctx stdcontext.Context, cliCtx *context, stackName string, dep *engine.Deployment, oldSpec, newSpec map[string]*stack.Service, services []string, out io.Writer) error {
	tracker := cliCtx.statusTracker()
	currentSpec := stack.CloneServiceMap(oldSpec)
	updated := make([]string, 0, len(services))

	for _, name := range services {
		svcSpec := newSpec[name]
		service, ok := dep.Service(name)
		if !ok {
			return fmt.Errorf("%w: service %s is not currently running", api.ErrServiceNotRunning, name)
		}

		replicas := service.Replicas()
		if replicas < 1 {
			replicas = 1
		}

		if out != nil {
			fmt.Fprintf(out, "Updating %s (%d replicas)\n", name, replicas)
		}
		updated = append(updated, name)

		strategy := "rolling"
		if svcSpec.Update != nil && svcSpec.Update.Strategy != "" {
			strategy = svcSpec.Update.Strategy
		}
		autoPromote := strategy != "canary"

		printCanaryPrompt := func(detail string) {
			detail = strings.TrimSpace(detail)
			if detail != "" {
				if out != nil {
					fmt.Fprintf(out, "Service %s canary ready (%s); run `orco promote %s` to continue rollout.\n", name, detail, name)
				}
			} else {
				if out != nil {
					fmt.Fprintf(out, "Service %s canary ready; run `orco promote %s` to continue rollout.\n", name, name)
				}
			}
		}

		updateStart := time.Now()
		errCh := make(chan error, 1)
		go func() {
			errCh <- runServiceUpdate(ctx, service, svcSpec, autoPromote)
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
					rollbackErr := rollbackUpdates(ctx, dep, oldSpec, updated)
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
			case <-ctx.Done():
				ticker.Stop()
				return ctx.Err()
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
				case <-ctx.Done():
					return ctx.Err()
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
		if out != nil {
			fmt.Fprintf(out, "Service %s ready (%d/%d replicas, %s)\n", name, ready, replicas, state)
		}
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
