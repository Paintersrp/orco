package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newUpCmd(ctx *context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Validate the stack and prepare services",
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := ctx.loadStack()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Stack %s loaded from %s\n", doc.File.Stack.Name, doc.Source)
			fmt.Fprintln(cmd.OutOrStdout(), "Startup order:")
			for i, svc := range doc.Graph.Services() {
				fmt.Fprintf(cmd.OutOrStdout(), "  %d. %s\n", i+1, svc)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Runtime orchestration is not yet implemented; this command currently performs validation and planning only.")
			return nil
		},
	}
	return cmd
}
