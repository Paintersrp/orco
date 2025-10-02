package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newLogsCmd(ctx *context) *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs [service]",
		Short: "Tail structured logs (planned)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := ctx.loadStack()
			if err != nil {
				return err
			}
			target := "all services"
			if len(args) == 1 {
				target = args[0]
				if _, ok := doc.File.Services[target]; !ok {
					return fmt.Errorf("unknown service %s", target)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Log streaming for %s is not yet implemented. Planned follow=%t\n", target, follow)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	return cmd
}
