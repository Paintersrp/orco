package cli

import (
	stdcontext "context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/Paintersrp/orco/internal/cliutil"
	"github.com/Paintersrp/orco/internal/engine"
)

func newLogsCmd(ctx *context) *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs [service]",
		Short: "Tail structured logs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := ctx.loadStack()
			if err != nil {
				return err
			}
			var filter string
			if len(args) == 1 {
				filter = args[0]
				if _, ok := doc.File.Services[filter]; !ok {
					return fmt.Errorf("unknown service %s", filter)
				}
			}

			events := make(chan engine.Event, 256)
			var printer sync.WaitGroup
			printer.Add(1)

			encoder := json.NewEncoder(cmd.OutOrStdout())
			go func() {
				defer printer.Done()
				for event := range events {
					if event.Type != engine.EventTypeLog {
						continue
					}
					if filter != "" && event.Service != filter {
						continue
					}
					cliutil.EncodeLogEvent(encoder, cmd.ErrOrStderr(), event)
				}
			}()

			orch := ctx.getOrchestrator()
			deployment, err := orch.Up(cmd.Context(), doc.File, doc.Graph, events)
			if err != nil {
				close(events)
				printer.Wait()
				return err
			}

			if follow {
				<-cmd.Context().Done()
			}

			var stopErr error
			if deployment != nil {
				stopCtx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 10*time.Second)
				stopErr = deployment.Stop(stopCtx, events)
				cancel()
			}

			close(events)
			printer.Wait()

			if stopErr != nil && !errors.Is(stopErr, stdcontext.Canceled) {
				return stopErr
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	return cmd
}
