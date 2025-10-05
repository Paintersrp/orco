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
	var historyLimit int

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

			out := cmd.OutOrStdout()
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SERVICE\tSTATE\tREADY\tREPL\tRESTARTS\tAGE\tMESSAGE")
			orderedServices := doc.File.ServicesSorted()
			for _, name := range orderedServices {
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
			fmt.Fprintf(out, "\nStack: %s (version %s)\n", doc.File.Stack.Name, doc.File.Version)
			fmt.Fprintf(out, "Validated at %s\n", time.Now().Format(time.RFC3339))

			if historyLimit > 0 {
				for _, name := range orderedServices {
					history := tracker.History(name, historyLimit)
					if len(history) == 0 {
						continue
					}
					fmt.Fprintf(out, "\n%s history:\n", name)
					for _, entry := range history {
						reason := entry.Reason
						if reason == "" {
							reason = "-"
						}
						fmt.Fprintf(out, "  %s  %-10s  %-20s  %s\n",
							entry.Timestamp.Format(time.RFC3339),
							formatStatusState(entry.Type),
							reason,
							entry.Message)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&historyLimit, "history", 0, "Show last N transitions per service")
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
