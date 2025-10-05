package engine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Paintersrp/orco/internal/stack"
)

var errNilStackFile = errors.New("cannot build graph from a nil stack file")

// Graph represents service dependencies.
type Graph struct {
	services map[string]*stack.Service
	edges    map[string][]string
	reverse  map[string][]string
	order    []string
}

// GraphServiceStatus captures the status information needed to render DOT output.
type GraphServiceStatus struct {
	State   EventType
	Ready   bool
	Message string
}

// DependencyMetadata conveys dependency attributes for DOT rendering.
type DependencyMetadata struct {
	Require        string
	BlockingReason string
}

const (
	dotColorReady   = "#c6f6d5"
	dotColorBlocked = "#fefcbf"
	dotColorFailed  = "#fed7d7"
	dotColorPending = "#e2e8f0"
)

// BuildGraph constructs the dependency graph and validates acyclicity.
func BuildGraph(doc *stack.StackFile) (*Graph, error) {
	if doc == nil {
		return nil, errNilStackFile
	}

	g := &Graph{
		services: make(map[string]*stack.Service, len(doc.Services)),
		edges:    make(map[string][]string, len(doc.Services)),
		reverse:  make(map[string][]string, len(doc.Services)),
	}
	for name, svc := range doc.Services {
		g.services[name] = svc
		if _, ok := g.edges[name]; !ok {
			g.edges[name] = nil
		}
		for _, dep := range svc.DependsOn {
			g.edges[name] = append(g.edges[name], dep.Target)
			g.reverse[dep.Target] = append(g.reverse[dep.Target], name)
			if _, ok := g.edges[dep.Target]; !ok {
				g.edges[dep.Target] = nil
			}
		}
	}

	order, err := topoSort(g.edges)
	if err != nil {
		return nil, err
	}
	g.order = order
	return g, nil
}

// Services returns service names in topological order.
func (g *Graph) Services() []string {
	out := make([]string, len(g.order))
	copy(out, g.order)
	return out
}

// Dependents returns services that depend on the provided service.
func (g *Graph) Dependents(name string) []string {
	deps := g.reverse[name]
	out := make([]string, len(deps))
	copy(out, deps)
	return out
}

// DOT renders the graph in Graphviz dot format with optional status and dependency metadata.
func (g *Graph) DOT(statuses map[string]GraphServiceStatus, deps map[string]map[string]DependencyMetadata) string {
	var b strings.Builder
	b.WriteString("digraph orco {\n")

	for _, svc := range g.order {
		status := statuses[svc]
		labelParts := []string{svc}
		statusLabel, statusMessage, fillColor := describeNodeStatus(status)
		if statusLabel != "" {
			labelParts = append(labelParts, statusLabel)
		}
		if statusMessage != "" {
			labelParts = append(labelParts, statusMessage)
		}
		label := formatDOTLabel(labelParts)
		attrs := []string{fmt.Sprintf("label=\"%s\"", label)}
		if fillColor != "" {
			attrs = append(attrs, "style=\"filled\"")
			attrs = append(attrs, fmt.Sprintf("fillcolor=\"%s\"", fillColor))
		}
		b.WriteString(fmt.Sprintf("  \"%s\" [%s];\n", escapeDOTID(svc), strings.Join(attrs, " ")))
	}

	for _, from := range g.order {
		seen := make(map[string]bool)
		if svc := g.services[from]; svc != nil {
			for _, dep := range svc.DependsOn {
				writeDOTEdge(&b, from, dep.Target, statuses, deps)
				seen[dep.Target] = true
			}
		}
		for _, to := range g.edges[from] {
			if seen[to] {
				continue
			}
			writeDOTEdge(&b, from, to, statuses, deps)
		}
	}

	b.WriteString("}\n")
	return b.String()
}

func describeNodeStatus(status GraphServiceStatus) (label string, message string, fillColor string) {
	if status.Ready {
		return "Ready", "", dotColorReady
	}

	switch status.State {
	case EventTypeBlocked:
		msg := status.Message
		if msg == "" {
			msg = "blocked"
		}
		return "Blocked", msg, dotColorBlocked
	case EventTypeFailed, EventTypeCrashed, EventTypeError:
		msg := status.Message
		if msg == "" {
			msg = "failure"
		}
		return "Failed", msg, dotColorFailed
	case "":
		return "Pending", "", dotColorPending
	default:
		return formatEventType(status.State), status.Message, dotColorPending
	}
}

func writeDOTEdge(b *strings.Builder, from, to string, statuses map[string]GraphServiceStatus, deps map[string]map[string]DependencyMetadata) {
	meta := DependencyMetadata{}
	if deps != nil {
		if m, ok := deps[from]; ok {
			if depMeta, ok := m[to]; ok {
				meta = depMeta
			}
		}
	}
	if meta.Require == "" {
		meta.Require = "ready"
	}

	if meta.BlockingReason == "" {
		if status, ok := statuses[from]; ok && status.State == EventTypeBlocked && status.Message != "" {
			if strings.Contains(status.Message, to) {
				meta.BlockingReason = status.Message
			}
		}
	}

	labelParts := []string{fmt.Sprintf("require=%s", meta.Require)}
	if meta.BlockingReason != "" {
		labelParts = append(labelParts, meta.BlockingReason)
	}

	label := formatDOTLabel(labelParts)
	attrs := []string{fmt.Sprintf("label=\"%s\"", label)}
	b.WriteString(fmt.Sprintf("  \"%s\" -> \"%s\" [%s];\n", escapeDOTID(from), escapeDOTID(to), strings.Join(attrs, " ")))
}

func formatDOTLabel(parts []string) string {
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		escaped = append(escaped, escapeDOTLabel(part))
	}
	if len(escaped) == 0 {
		return ""
	}
	return strings.Join(escaped, "\\n")
}

func escapeDOTLabel(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

func escapeDOTID(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return s
}

func formatEventType(t EventType) string {
	if t == "" {
		return "-"
	}
	s := string(t)
	if len(s) <= 1 {
		return strings.ToUpper(s)
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func topoSort(edges map[string][]string) ([]string, error) {
	indegree := make(map[string]int)
	for from := range edges {
		if _, ok := indegree[from]; !ok {
			indegree[from] = 0
		}
		for _, to := range edges[from] {
			indegree[to]++
		}
	}
	queue := make([]string, 0, len(indegree))
	for node, deg := range indegree {
		if deg == 0 {
			queue = append(queue, node)
		}
	}
	order := make([]string, 0, len(indegree))
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		order = append(order, node)
		for _, dep := range edges[node] {
			indegree[dep]--
			if indegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}
	if len(order) != len(indegree) {
		cycle := detectCycle(edges)
		return nil, fmt.Errorf("dependency cycle detected: %s", strings.Join(cycle, " -> "))
	}
	return order, nil
}

func detectCycle(edges map[string][]string) []string {
	visited := make(map[string]bool)
	stack := make([]string, 0)

	var dfs func(string) []string
	dfs = func(node string) []string {
		visited[node] = true
		stack = append(stack, node)
		for _, next := range edges[node] {
			onStack := false
			for _, cur := range stack {
				if cur == next {
					onStack = true
					break
				}
			}
			if onStack {
				return appendStack(stack, next)
			}
			if !visited[next] {
				if cycle := dfs(next); cycle != nil {
					return cycle
				}
			}
		}
		stack = stack[:len(stack)-1]
		return nil
	}

	for node := range edges {
		if !visited[node] {
			if cycle := dfs(node); cycle != nil {
				return cycle
			}
		}
	}
	return nil
}

func appendStack(stack []string, target string) []string {
	idx := -1
	for i, node := range stack {
		if node == target {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil
	}
	out := make([]string, 0, len(stack)-idx+1)
	for i := idx; i < len(stack); i++ {
		out = append(out, stack[i])
	}
	out = append(out, target)
	return out
}
