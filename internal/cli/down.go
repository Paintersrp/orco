package cli

import (
	stdcontext "context"
	"fmt"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/Paintersrp/orco/internal/engine"
)

func newDownCmd(ctx *context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Stop services defined in the stack",
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := ctx.loadStack()
			if err != nil {
				return err
			}

			events := make(chan engine.Event, 64)
			var printer sync.WaitGroup
			printer.Add(1)
			go func() {
				defer printer.Done()
				printEvents(cmd.OutOrStdout(), cmd.ErrOrStderr(), events)
			}()
			defer func() {
				close(events)
				printer.Wait()
			}()

			orch := ctx.getOrchestrator()

			deployment, err := orch.Up(cmd.Context(), doc.File, doc.Graph, events)
			if err != nil {
				return err
			}
			if deployment == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Stack %s has no services to stop.\n", doc.File.Stack.Name)
				return nil
			}

			stopCtx, cancel := stdcontext.WithTimeout(stdcontext.WithoutCancel(cmd.Context()), 10*time.Second)
			defer cancel()

			if err := deployment.Stop(stopCtx, events); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "shutdown error: %v\n", err)
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Stack %s shut down.\n", doc.File.Stack.Name)
			return nil
		},
	}
	return cmd
}
