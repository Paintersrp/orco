package cli

import (
	"fmt"

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
			fmt.Fprintf(cmd.OutOrStdout(), "Planned restart for %s (strategy=%s, replicas=%d). Implementation pending.\n", svcName, strategy, svc.Replicas)
			return nil
		},
	}
	return cmd
}
