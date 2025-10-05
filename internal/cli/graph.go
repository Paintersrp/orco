package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Paintersrp/orco/internal/cliutil"
	"github.com/Paintersrp/orco/internal/config"
	"github.com/Paintersrp/orco/internal/engine"
)

func newGraphCmd(ctx *context) *cobra.Command {
	var dot bool
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Render the dependency graph",
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := ctx.loadStack()
			if err != nil {
				return err
			}
			if dot {
				fmt.Fprint(cmd.OutOrStdout(), doc.Graph.DOT())
				return nil
			}
			tracker := ctx.statusTracker()
			snapshot := tracker.Snapshot()

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
		},
	}
	cmd.Flags().BoolVar(&dot, "dot", false, "Render in Graphviz DOT format")
	return cmd
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
