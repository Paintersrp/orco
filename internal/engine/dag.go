package engine

import (
	"fmt"
	"strings"

	"github.com/Paintersrp/orco/internal/stack"
)

// Graph represents service dependencies.
type Graph struct {
	services map[string]*stack.Service
	edges    map[string][]string
	reverse  map[string][]string
	order    []string
}

// BuildGraph constructs the dependency graph and validates acyclicity.
func BuildGraph(doc *stack.StackFile) (*Graph, error) {
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

// DOT renders the graph in Graphviz dot format.
func (g *Graph) DOT() string {
	var b strings.Builder
	b.WriteString("digraph orco {\n")
	for svc := range g.services {
		b.WriteString(fmt.Sprintf("  \"%s\";\n", svc))
	}
	for from, tos := range g.edges {
		for _, to := range tos {
			b.WriteString(fmt.Sprintf("  \"%s\" -> \"%s\";\n", from, to))
		}
	}
	b.WriteString("}\n")
	return b.String()
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
