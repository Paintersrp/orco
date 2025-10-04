package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newTuiCmd(ctx *context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Launch the interactive status interface",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !supportsInteractiveOutput(cmd) {
				return fmt.Errorf("tui requires an interactive terminal")
			}

			doc, err := ctx.loadStack()
			if err != nil {
				return err
			}

			return runStackTUI(cmd, ctx, doc)
		},
	}

	return cmd
}
