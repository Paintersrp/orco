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

			deployment, stackName := ctx.currentDeploymentInfo()
			if doc != nil && doc.File != nil && doc.File.Stack.Name != "" {
				stackName = doc.File.Stack.Name
			}

			if deployment == nil {
				if stackName != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "Stack %s has no running services.\n", stackName)
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "No running services found.")
				}
				return nil
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

			stopCtx, cancel := stdcontext.WithTimeout(stdcontext.WithoutCancel(cmd.Context()), 10*time.Second)
			defer cancel()

			if err := deployment.Stop(stopCtx, events); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "shutdown error: %v\n", err)
				return err
			}

			ctx.clearDeployment(deployment)

			if stackName != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Stack %s shut down.\n", stackName)
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "Stack shut down.")
			}
			return nil
		},
	}
	return cmd
}
