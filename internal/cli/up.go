package cli

import (
	stdcontext "context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/example/orco/internal/engine"
)

func newUpCmd(ctx *context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Start services defined in the stack",
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

			fmt.Fprintln(cmd.OutOrStdout(), "All services reported ready.")

			stopCtx, cancel := stdcontext.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			if err := deployment.Stop(stopCtx, events); err != nil {
				return err
			}

			fmt.Fprintln(cmd.OutOrStdout(), "Services shut down cleanly.")
			return nil
		},
	}
	return cmd
}

func printEvents(stdout, stderr io.Writer, events <-chan engine.Event) {
	encoder := json.NewEncoder(stdout)
	for event := range events {
		switch event.Type {
		case engine.EventTypeLog:
			encodeLogEvent(encoder, stderr, event)
		case engine.EventTypeError:
			if event.Err != nil {
				fmt.Fprintf(stderr, "error: %s %s: %v\n", event.Service, event.Message, event.Err)
			} else {
				fmt.Fprintf(stderr, "error: %s %s\n", event.Service, event.Message)
			}
		default:
			label := formatEventType(event.Type)
			if event.Message != "" {
				fmt.Fprintf(stdout, "%s %s: %s\n", label, event.Service, event.Message)
			} else {
				fmt.Fprintf(stdout, "%s %s\n", label, event.Service)
			}
		}
	}
}

func formatEventType(t engine.EventType) string {
	s := string(t)
	if s == "" {
		return ""
	}
	if len(s) == 1 {
		return strings.ToUpper(s)
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
