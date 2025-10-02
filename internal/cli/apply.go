package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newApplyCmd(ctx *context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Reconcile stack changes (planned)",
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := ctx.loadStack()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Apply is not yet implemented. Stack %s parsed successfully.\n", doc.File.Stack.Name)
			return nil
		},
	}
	return cmd
}
