package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Paintersrp/orco/internal/engine"
)

func newStatusCmd(ctx *context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Display a summary of services defined in the stack",
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := ctx.loadStack()
			if err != nil {
				return err
			}
			tracker := ctx.statusTracker()
			snapshot := tracker.Snapshot()

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SERVICE\tSTATE\tREADY\tREPL\tRESTARTS\tAGE\tMESSAGE")
			for _, name := range doc.File.ServicesSorted() {
				status, ok := snapshot[name]
				state := formatStatusState(status.State)
				ready := "-"
				replicas := "-"
				restarts := 0
				age := "-"
				message := "-"
				if ok {
					if status.FirstSeen.IsZero() {
						age = "-"
					} else {
						ageDur := time.Since(status.FirstSeen)
						if ageDur < 0 {
							ageDur = 0
						}
						ageDur = ageDur.Truncate(time.Second)
						age = ageDur.String()
					}
					readyState := "No"
					if status.Ready {
						readyState = "Yes"
					}
					totalReplicas := status.Replicas
					if totalReplicas > 0 {
						readyReplicas := status.ReadyReplicas
						if readyReplicas > totalReplicas {
							readyReplicas = totalReplicas
						}
						ready = fmt.Sprintf("%s (%d/%d)", readyState, readyReplicas, totalReplicas)
						replicas = fmt.Sprintf("%d", totalReplicas)
					} else {
						ready = readyState
					}
					restarts = status.Restarts
					if status.Message != "" {
						message = status.Message
					} else {
						message = "-"
					}
					state = formatStatusState(status.State)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", name, state, ready, replicas, restarts, age, message)
			}
			w.Flush()
			fmt.Fprintf(cmd.OutOrStdout(), "\nStack: %s (version %s)\n", doc.File.Stack.Name, doc.File.Version)
			fmt.Fprintf(cmd.OutOrStdout(), "Validated at %s\n", time.Now().Format(time.RFC3339))
			return nil
		},
	}
	return cmd
}

func formatStatusState(t engine.EventType) string {
	if t == "" {
		return "-"
	}
	s := string(t)
	if len(s) <= 1 {
		return strings.ToUpper(s)
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
