package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Paintersrp/orco/internal/engine"
)

func newPromoteCmd(ctx *context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "promote [service]",
		Short: "Promote a canary rollout to all replicas",
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
			if svc.Update != nil && strings.TrimSpace(svc.Update.Strategy) != "" {
				strategy = strings.ToLower(strings.TrimSpace(svc.Update.Strategy))
			}
			if strategy != "canary" {
				return fmt.Errorf("service %s does not use the canary update strategy", svcName)
			}

			dep, stackName := ctx.currentDeploymentInfo()
			if dep == nil {
				return errors.New("no active deployment to promote")
			}
			if stackName != "" && stackName != doc.File.Stack.Name {
				return fmt.Errorf("active deployment %s does not match stack %s", stackName, doc.File.Stack.Name)
			}

			if _, ok := dep.Service(svcName); !ok {
				return fmt.Errorf("service %s is not currently running", svcName)
			}

			status, tracked := ctx.statusTracker().Snapshot()[svcName]
			if !tracked || status.State != engine.EventTypeCanary {
				return fmt.Errorf("service %s has no canary awaiting promotion", svcName)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Promoting service %s...\n", svcName)

			if err := dep.PromoteService(cmd.Context(), svcName); err != nil {
				if errors.Is(err, engine.ErrNoPromotionPending) {
					return fmt.Errorf("service %s has no pending promotion", svcName)
				}
				return fmt.Errorf("promote %s: %w", svcName, err)
			}

			deadline := time.Now().Add(500 * time.Millisecond)
			for {
				snap := ctx.statusTracker().Snapshot()[svcName]
				if snap.State == engine.EventTypePromoted {
					fmt.Fprintf(cmd.OutOrStdout(), "Service %s promotion complete.\n", svcName)
					return nil
				}
				if time.Now().After(deadline) {
					break
				}
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(10 * time.Millisecond):
				}
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Service %s promotion complete.\n", svcName)
			return nil
		},
	}
	return cmd
}
