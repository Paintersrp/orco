package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newDownCmd(ctx *context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Plan stack shutdown in reverse dependency order",
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := ctx.loadStack()
			if err != nil {
				return err
			}
			order := doc.Graph.Services()
			fmt.Fprintf(cmd.OutOrStdout(), "Shutdown order for stack %s:\n", doc.File.Stack.Name)
			for i := len(order) - 1; i >= 0; i-- {
				fmt.Fprintf(cmd.OutOrStdout(), "  %d. %s\n", len(order)-i, order[i])
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Process orchestration is forthcoming; down currently reports the planned order.")
			return nil
		},
	}
	return cmd
}
