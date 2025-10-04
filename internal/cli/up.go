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
	"golang.org/x/term"

	"github.com/Paintersrp/orco/internal/cliutil"
	"github.com/Paintersrp/orco/internal/engine"
	"github.com/Paintersrp/orco/internal/tui"
)

func newUpCmd(ctx *context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Start services defined in the stack",
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			doc, err := ctx.loadStack()
			if err != nil {
				return err
			}

			if !supportsInteractiveOutput(cmd) {
				return runUpNonInteractive(cmd, ctx, doc)
			}
			return runStackTUI(cmd, ctx, doc)
		},
	}
	return cmd
}

func runStackTUI(cmd *cobra.Command, ctx *context, doc *cliutil.StackDocument) (retErr error) {
	ui := tui.New()
	uiErrCh := make(chan error, 1)
	go func() {
		uiErrCh <- ui.Run(cmd.Context())
	}()

	runCtx, runCancel := stdcontext.WithCancel(stdcontext.WithoutCancel(cmd.Context()))
	defer runCancel()

	cancelGuard := stdcontext.AfterFunc(cmd.Context(), runCancel)
	defer cancelGuard()

	var deployment *engine.Deployment
	defer func() {
		if deployment != nil {
			stopCtx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 10*time.Second)
			if err := deployment.Stop(stopCtx, ui.EventSink()); err != nil && retErr == nil {
				retErr = err
			}
			cancel()
			ctx.clearDeployment(deployment)
		}
		ui.CloseEvents()
		ui.Stop()
		if uiErr := <-uiErrCh; uiErr != nil && retErr == nil {
			retErr = uiErr
		}
	}()

	orch := ctx.getOrchestrator()
	var depErr error
	deployment, depErr = orch.Up(runCtx, doc.File, doc.Graph, ui.EventSink())
	cancelGuard()
	if depErr != nil {
		return depErr
	}

	ctx.setDeployment(deployment)

	select {
	case <-cmd.Context().Done():
	case <-ui.Done():
	}

	return retErr
}

func runUpInteractive(cmd *cobra.Command, ctx *context, doc *cliutil.StackDocument) (retErr error) {
	return runStackTUI(cmd, ctx, doc)
}

func runUpNonInteractive(cmd *cobra.Command, ctx *context, doc *cliutil.StackDocument) (retErr error) {
	events := make(chan engine.Event, 64)
	trackedEvents := ctx.trackEvents(events, cap(events))
	var printer sync.WaitGroup
	printer.Add(1)
	go func() {
		defer printer.Done()
		printEvents(cmd.OutOrStdout(), cmd.ErrOrStderr(), trackedEvents)
	}()

	runCtx, runCancel := stdcontext.WithCancel(stdcontext.WithoutCancel(cmd.Context()))
	defer runCancel()

	cancelGuard := stdcontext.AfterFunc(cmd.Context(), runCancel)
	defer cancelGuard()

	var deployment *engine.Deployment
	defer func() {
		if deployment != nil {
			stopCtx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 10*time.Second)
			if err := deployment.Stop(stopCtx, events); err != nil && retErr == nil {
				retErr = err
			} else if err == nil {
				fmt.Fprintln(cmd.OutOrStdout(), "Services shut down cleanly.")
			}
			cancel()
			ctx.clearDeployment(deployment)
		}
		close(events)
		printer.Wait()
	}()

	orch := ctx.getOrchestrator()
	var depErr error
	deployment, depErr = orch.Up(runCtx, doc.File, doc.Graph, events)
	cancelGuard()
	if depErr != nil {
		return depErr
	}

	ctx.setDeployment(deployment)

	fmt.Fprintln(cmd.OutOrStdout(), "All services reported ready.")

	<-cmd.Context().Done()

	return retErr
}

func supportsInteractiveOutput(cmd *cobra.Command) bool {
	return isTerminalWriter(cmd.OutOrStdout()) && isTerminalWriter(cmd.ErrOrStderr())
}

func isTerminalWriter(w io.Writer) bool {
	type fdWriter interface {
		Fd() uintptr
	}
	f, ok := w.(fdWriter)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

func printEvents(stdout, stderr io.Writer, events <-chan engine.Event) {
	encoder := json.NewEncoder(stdout)
	for event := range events {
		switch event.Type {
		case engine.EventTypeLog:
			cliutil.EncodeLogEvent(encoder, stderr, event)
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
