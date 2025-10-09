package cli

import (
        "encoding/json"
        "fmt"
        "sort"
        "strings"

        "github.com/spf13/cobra"

        "github.com/Paintersrp/orco/internal/cliutil"
        "github.com/Paintersrp/orco/internal/config"
        "github.com/Paintersrp/orco/internal/engine"
)

func newGraphCmd(ctx *context) *cobra.Command {
        var outputFormat string
        cmd := &cobra.Command{
                Use:   "graph",
                Short: "Render the dependency graph",
                RunE: func(cmd *cobra.Command, args []string) error {
                        doc, err := ctx.loadStack()
			if err != nil {
				return err
			}
                        tracker := ctx.statusTracker()
                        snapshot := tracker.Snapshot()

                        format := strings.ToLower(outputFormat)
                        if format == "" {
                                format = "text"
                        }

                        switch format {
                        case "text":
                                var b strings.Builder
                                services := doc.Graph.Services()
                                for i, svc := range services {
                                        if i > 0 {
                                                b.WriteByte('\n')
                                        }
                                        renderServiceTree(&b, doc, snapshot, svc, "", nil, true, make(map[string]bool))
                                }

                                fmt.Fprint(cmd.OutOrStdout(), b.String())
                                return nil
                        case "dot":
                                statuses := make(map[string]engine.GraphServiceStatus, len(snapshot))
                                for name, status := range snapshot {
                                        statuses[name] = engine.GraphServiceStatus{State: status.State, Ready: status.Ready, Message: status.Message}
                                }

                                depMeta := make(map[string]map[string]engine.DependencyMetadata)
                                for name, svc := range doc.File.Services {
                                        if svc == nil {
                                                continue
                                        }
                                        for _, dep := range svc.DependsOn {
                                                require := dep.Require
                                                if require == "" {
                                                        require = "ready"
                                                }
                                                meta := engine.DependencyMetadata{Require: require}
                                                if status, ok := snapshot[name]; ok && status.State == engine.EventTypeBlocked && status.Message != "" {
                                                        if strings.Contains(status.Message, dep.Target) {
                                                                meta.BlockingReason = status.Message
                                                        }
                                                }
                                                if _, ok := depMeta[name]; !ok {
                                                        depMeta[name] = make(map[string]engine.DependencyMetadata)
                                                }
                                                depMeta[name][dep.Target] = meta
                                        }
                                }

                                fmt.Fprint(cmd.OutOrStdout(), doc.Graph.DOT(statuses, depMeta))
                                return nil
                        case "json":
                                payload := buildGraphJSON(doc, snapshot)
                                encoder := json.NewEncoder(cmd.OutOrStdout())
                                return encoder.Encode(payload)
                        default:
                                return fmt.Errorf("unsupported output format %q", format)
                        }
                },
        }
        cmd.Flags().StringVarP(&outputFormat, "output", "o", "text", "Output format (text|dot|json)")
        return cmd
}

func buildGraphJSON(doc *cliutil.StackDocument, snapshot map[string]ServiceStatus) graphJSONDocument {
        ordered := doc.File.ServicesSorted()
        seen := make(map[string]struct{}, len(ordered))
        nodes := make([]graphJSONNode, 0, len(ordered)+len(snapshot))
        for _, name := range ordered {
                nodes = append(nodes, graphJSONNodeFromStatus(name, snapshot[name]))
                seen[name] = struct{}{}
        }

        extras := make([]string, 0)
        for name := range snapshot {
                if _, ok := seen[name]; ok {
                        continue
                }
                extras = append(extras, name)
        }
        sort.Strings(extras)
        for _, name := range extras {
                nodes = append(nodes, graphJSONNodeFromStatus(name, snapshot[name]))
        }

        edges := make([]graphJSONEdge, 0)
        for _, name := range ordered {
                svc := doc.File.Services[name]
                if svc == nil {
                        continue
                }
                deps := svc.DependsOn
                if len(deps) == 0 {
                        continue
                }
                for _, dep := range deps {
                        require := dep.Require
                        if require == "" {
                                require = "ready"
                        }
                        edge := graphJSONEdge{
                                From:    name,
                                To:      dep.Target,
                                Require: require,
                        }
                        if dep.Timeout.IsSet() {
                                edge.Timeout = dep.Timeout.Duration.String()
                        }
                        if status, ok := snapshot[name]; ok && status.State == engine.EventTypeBlocked && status.Message != "" {
                                if strings.Contains(status.Message, dep.Target) {
                                        edge.BlockingReason = status.Message
                                }
                        }
                        edges = append(edges, edge)
                }
        }

        sort.Slice(edges, func(i, j int) bool {
                if edges[i].From != edges[j].From {
                        return edges[i].From < edges[j].From
                }
                if edges[i].To != edges[j].To {
                        return edges[i].To < edges[j].To
                }
                if edges[i].Require != edges[j].Require {
                        return edges[i].Require < edges[j].Require
                }
                return edges[i].Timeout < edges[j].Timeout
        })

        return graphJSONDocument{
                Stack:   doc.File.Stack.Name,
                Version: doc.File.Version,
                Nodes:   nodes,
                Edges:   edges,
        }
}

func graphJSONNodeFromStatus(name string, status ServiceStatus) graphJSONNode {
        node := graphJSONNode{
                Name:    name,
                State:   status.State,
                Ready:   status.Ready,
                Message: status.Message,
        }
        if status.Restarts > 0 {
                node.Restarts = status.Restarts
        }
        if status.Replicas > 0 {
                node.Replicas = status.Replicas
        }
        if status.ReadyReplicas > 0 {
                node.ReadyReplicas = status.ReadyReplicas
        }
        return node
}

func renderServiceTree(b *strings.Builder, doc *cliutil.StackDocument, snapshot map[string]ServiceStatus, svc string, prefix string, dep *config.DepEdge, isLast bool, visited map[string]bool) {
        linePrefix := prefix
        if dep != nil {
                if isLast {
			linePrefix += "└─ "
		} else {
			linePrefix += "├─ "
		}
	}

	statusLabel, statusMessage := describeServiceStatus(snapshot, svc)
	statusText := statusLabel
	if statusMessage != "" {
		statusText = fmt.Sprintf("%s: %s", statusLabel, statusMessage)
	}

	annotation := ""
	if dep != nil {
		annotation = formatDependencyAnnotation(*dep)
	}

	fmt.Fprintf(b, "%s%s%s [%s]\n", linePrefix, svc, annotation, statusText)

	if visited[svc] {
		return
	}
	visited[svc] = true
	defer delete(visited, svc)

	serviceSpec, ok := doc.File.Services[svc]
	if !ok || serviceSpec == nil {
		return
	}

	deps := serviceSpec.DependsOn
	if len(deps) == 0 {
		return
	}

	nextPrefix := prefix
	if dep != nil {
		if isLast {
			nextPrefix += "   "
		} else {
			nextPrefix += "│  "
		}
	}

	for i, child := range deps {
		child := child
		renderServiceTree(b, doc, snapshot, child.Target, nextPrefix, &child, i == len(deps)-1, visited)
	}
}

func describeServiceStatus(snapshot map[string]ServiceStatus, name string) (string, string) {
	status, ok := snapshot[name]
	if !ok {
		return "Pending", ""
	}
	if status.Ready {
		return "Ready", ""
	}
	switch status.State {
	case engine.EventTypeBlocked:
		message := status.Message
		if message == "" {
			message = "blocked"
		}
		return "Blocked", message
	case engine.EventTypeFailed, engine.EventTypeCrashed, engine.EventTypeError:
		message := status.Message
		if message == "" {
			message = "failure"
		}
		return "Failed", message
	default:
		label := formatStatusState(status.State)
		message := ""
		if status.Message != "" {
			message = status.Message
		}
		if label == "-" {
			label = "Pending"
		}
		return label, message
	}
}

func formatDependencyAnnotation(dep config.DepEdge) string {
        require := dep.Require
        if require == "" {
                require = "ready"
        }
        parts := []string{fmt.Sprintf("require=%s", require)}
        if dep.Timeout.IsSet() {
                parts = append(parts, fmt.Sprintf("timeout=%s", dep.Timeout.Duration))
        }
        return fmt.Sprintf(" (%s)", strings.Join(parts, ", "))
}

type graphJSONDocument struct {
        Stack   string           `json:"stack"`
        Version string           `json:"version"`
        Nodes   []graphJSONNode  `json:"nodes"`
        Edges   []graphJSONEdge  `json:"edges"`
}

type graphJSONNode struct {
        Name          string           `json:"name"`
        State         engine.EventType `json:"state"`
        Ready         bool             `json:"ready"`
        Message       string           `json:"message,omitempty"`
        Restarts      int              `json:"restarts,omitempty"`
        Replicas      int              `json:"replicas,omitempty"`
        ReadyReplicas int              `json:"readyReplicas,omitempty"`
}

type graphJSONEdge struct {
        From           string `json:"from"`
        To             string `json:"to"`
        Require        string `json:"require"`
        Timeout        string `json:"timeout,omitempty"`
        BlockingReason string `json:"blockingReason,omitempty"`
}
