package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// NewRootCmd constructs the root command.
func NewRootCmd() *cobra.Command {
	var stackFile string

	root := &cobra.Command{
		Use:   "orco",
		Short: "Single-node orchestration engine",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}

	root.PersistentFlags().StringVarP(&stackFile, "file", "f", "stack.yaml", "Path to stack definition")

	ctx := &context{stackFile: &stackFile}
	root.AddCommand(newUpCmd(ctx))
	root.AddCommand(newDownCmd(ctx))
	root.AddCommand(newStatusCmd(ctx))
	root.AddCommand(newLogsCmd(ctx))
	root.AddCommand(newGraphCmd(ctx))
	root.AddCommand(newRestartCmd(ctx))
	root.AddCommand(newApplyCmd(ctx))

	root.SilenceUsage = true
	root.SilenceErrors = true

	return root
}

// Execute runs the CLI entrypoint.
func Execute() {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type context struct {
	stackFile *string
}

func (c *context) loadStack() (*stackDocument, error) {
	return loadStackFromFile(*c.stackFile)
}
