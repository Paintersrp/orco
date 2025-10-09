package cli

import (
        "encoding/json"
        "fmt"
        "strings"
        "text/tabwriter"
        "time"

        "github.com/spf13/cobra"

        units "github.com/docker/go-units"

        "github.com/Paintersrp/orco/internal/api"
        "github.com/Paintersrp/orco/internal/cliutil"
        "github.com/Paintersrp/orco/internal/config"
        "github.com/Paintersrp/orco/internal/engine"
        "github.com/Paintersrp/orco/internal/resources"
)

func newStatusCmd(ctx *context) *cobra.Command {
        var historyLimit int
        var outputFormat string

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

                        format := strings.ToLower(outputFormat)
                        if format == "" {
                                format = "text"
                        }

                        switch format {
                        case "text":
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
                        case "json":
                                out := cmd.OutOrStdout()
                                report, err := buildStatusReport(doc, tracker, snapshot, historyLimit)
                                if err != nil {
                                        return err
                                }
                                encoder := json.NewEncoder(out)
                                return encoder.Encode(report)
                        default:
                                return fmt.Errorf("unsupported output format %q", format)
                        }
                },
        }
        cmd.Flags().IntVar(&historyLimit, "history", 0, "Show last N transitions per service")
        cmd.Flags().StringVarP(&outputFormat, "output", "o", "text", "Output format (text|json)")
        return cmd
}

func buildStatusReport(doc *cliutil.StackDocument, tracker *statusTracker, snapshot map[string]ServiceStatus, historyLimit int) (*api.StatusReport, error) {
        if doc == nil || doc.File == nil {
                return nil, fmt.Errorf("invalid stack document")
        }

        historyDepth := defaultHistoryDepth
        if historyLimit > 0 {
                historyDepth = historyLimit
        }

        services := make(map[string]api.ServiceReport, len(doc.File.Services)+len(snapshot))
        for _, name := range doc.File.ServicesSorted() {
                status, ok := snapshot[name]
                if !ok {
                        services[name] = api.ServiceReport{Name: name}
                        continue
                }
                history := tracker.History(name, historyDepth)
                apiHistory := make([]api.ServiceTransition, 0, len(history))
                for _, entry := range history {
                        apiHistory = append(apiHistory, api.ServiceTransition{
                                Timestamp: entry.Timestamp,
                                Type:      entry.Type,
                                Reason:    entry.Reason,
                                Message:   entry.Message,
                        })
                }
                lastReason := ""
                if len(apiHistory) > 0 {
                        lastReason = apiHistory[len(apiHistory)-1].Reason
                }
                services[name] = api.ServiceReport{
                        Name:          status.Name,
                        State:         status.State,
                        Ready:         status.Ready,
                        Restarts:      status.Restarts,
                        Replicas:      status.Replicas,
                        ReadyReplicas: status.ReadyReplicas,
                        Message:       status.Message,
                        FirstSeen:     status.FirstSeen,
                        LastEvent:     status.LastEvent,
                        Resources: api.ResourceHint{
                                CPU:    status.Resources.CPU,
                                Memory: status.Resources.Memory,
                        },
                        History:    apiHistory,
                        LastReason: lastReason,
                }
        }

        for name, status := range snapshot {
                if _, ok := services[name]; ok {
                        continue
                }
                history := tracker.History(name, historyDepth)
                apiHistory := make([]api.ServiceTransition, 0, len(history))
                for _, entry := range history {
                        apiHistory = append(apiHistory, api.ServiceTransition{
                                Timestamp: entry.Timestamp,
                                Type:      entry.Type,
                                Reason:    entry.Reason,
                                Message:   entry.Message,
                        })
                }
                lastReason := ""
                if len(apiHistory) > 0 {
                        lastReason = apiHistory[len(apiHistory)-1].Reason
                }
                services[name] = api.ServiceReport{
                        Name:          status.Name,
                        State:         status.State,
                        Ready:         status.Ready,
                        Restarts:      status.Restarts,
                        Replicas:      status.Replicas,
                        ReadyReplicas: status.ReadyReplicas,
                        Message:       status.Message,
                        FirstSeen:     status.FirstSeen,
                        LastEvent:     status.LastEvent,
                        Resources: api.ResourceHint{
                                CPU:    status.Resources.CPU,
                                Memory: status.Resources.Memory,
                        },
                        History:    apiHistory,
                        LastReason: lastReason,
                }
        }

        report := &api.StatusReport{
                Stack:       doc.File.Stack.Name,
                Version:     doc.File.Version,
                GeneratedAt: time.Now(),
                Services:    services,
        }
        return report, nil
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
