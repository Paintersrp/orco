package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/example/orco/internal/cliutil"
	"github.com/example/orco/internal/engine"
	"github.com/example/orco/internal/runtime"
	"github.com/example/orco/internal/runtime/docker"
	"github.com/example/orco/internal/runtime/process"
)

func NewRootCmd() *cobra.Command {
	var stackFile string

	root := &cobra.Command{
		Use:   "orco",
		Short: "Single-node orchestration engine",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}

	root.PersistentFlags().
		StringVarP(&stackFile, "file", "f", "stack.yaml", "Path to stack definition")

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
	stackFile    *string
	orchestrator *engine.Orchestrator
}

func (c *context) loadStack() (*cliutil.StackDocument, error) {
	return cliutil.LoadStackFromFile(*c.stackFile)
}

func (c *context) getOrchestrator() *engine.Orchestrator {
	if c.orchestrator == nil {
		c.orchestrator = engine.NewOrchestrator(runtime.Registry{
			"docker":  docker.New(),
			"process": process.New(),
		})
	}
	return c.orchestrator
}
