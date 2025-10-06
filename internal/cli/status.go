package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	units "github.com/docker/go-units"

	"github.com/Paintersrp/orco/internal/config"
	"github.com/Paintersrp/orco/internal/engine"
	"github.com/Paintersrp/orco/internal/resources"
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

			orderedServices := doc.File.ServicesSorted()
			resourceHints := make(map[string]ResourceHint, len(orderedServices))
			for _, name := range orderedServices {
				svc := doc.File.Services[name]
				resourceHints[name] = buildResourceHint(svc)
			}
			tracker.SetResourceHints(resourceHints)
			snapshot := tracker.Snapshot()

			out := cmd.OutOrStdout()
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SERVICE\tSTATE\tREADY\tREPL\tCPU/MEM\tRESTARTS\tAGE\tMESSAGE")
			for _, name := range orderedServices {
				status, ok := snapshot[name]
				state := formatStatusState(status.State)
				ready := "-"
				replicas := "-"
				restarts := 0
				age := "-"
				message := "-"
				resource := formatCombinedResourceHint(resourceHints[name])
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
					resource = formatCombinedResourceHint(status.Resources)
					if resource == "-" {
						resource = formatCombinedResourceHint(resourceHints[name])
					}
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n", name, state, ready, replicas, resource, restarts, age, message)
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

func buildResourceHint(svc *config.ServiceSpec) ResourceHint {
	if svc == nil || svc.Resources == nil {
		return ResourceHint{}
	}

	hint := ResourceHint{}
	if cpu := formatCPUHint(svc.Resources.CPU); cpu != "" {
		hint.CPU = cpu
	}

	if memory := formatMemoryHint(svc.Resources.Memory); memory != "" {
		hint.Memory = memory
	} else if reservation := formatMemoryHint(svc.Resources.MemoryReservation); reservation != "" {
		hint.Memory = fmt.Sprintf("%s (reservation)", reservation)
	}

	return hint
}

func formatCombinedResourceHint(hint ResourceHint) string {
	cpu := strings.TrimSpace(hint.CPU)
	memory := strings.TrimSpace(hint.Memory)
	switch {
	case cpu != "" && memory != "":
		return fmt.Sprintf("%s / %s", cpu, memory)
	case cpu != "":
		return cpu
	case memory != "":
		return memory
	default:
		return "-"
	}
}

func formatCPUHint(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	nano, err := resources.ParseCPU(trimmed)
	if err != nil {
		return trimmed
	}
	if nano%resources.NanoCPUs == 0 {
		return fmt.Sprintf("%d", nano/resources.NanoCPUs)
	}
	milliUnit := resources.NanoCPUs / 1000
	if nano < resources.NanoCPUs && nano%int64(milliUnit) == 0 {
		return fmt.Sprintf("%dm", nano/int64(milliUnit))
	}
	cores := float64(nano) / float64(resources.NanoCPUs)
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", cores), "0"), ".")
}

func formatMemoryHint(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	bytes, err := resources.ParseMemory(trimmed)
	if err != nil {
		return trimmed
	}
	return units.BytesSize(float64(bytes))
}
