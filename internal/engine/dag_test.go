package engine

import (
	"strings"
	"testing"

	"github.com/Paintersrp/orco/internal/config"
	"github.com/Paintersrp/orco/internal/stack"
)

func TestBuildGraphNilStackFile(t *testing.T) {
	t.Parallel()

	_, err := BuildGraph(nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if err != errNilStackFile {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGraphDOTIncludesStatusStylingAndMetadata(t *testing.T) {
	t.Parallel()

	doc := &stack.StackFile{
		Services: map[string]*stack.Service{
			"api": {
				DependsOn: []config.DepEdge{{Target: "db"}},
			},
			"db": {
				DependsOn: []config.DepEdge{{Target: "storage", Require: "started"}},
			},
			"storage": {},
		},
	}

	graph, err := BuildGraph(doc)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}

	statuses := map[string]GraphServiceStatus{
		"api":     {State: EventTypeReady, Ready: true},
		"db":      {State: EventTypeBlocked, Message: "blocked waiting for storage (started)"},
		"storage": {State: EventTypeFailed, Message: "probe failed"},
	}

	deps := map[string]map[string]DependencyMetadata{
		"api": {
			"db": {Require: "ready"},
		},
		"db": {
			"storage": {Require: "started", BlockingReason: "blocked waiting for storage (started)"},
		},
	}

	dot := graph.DOT(statuses, deps)

	expectations := []string{
		"\"api\" [label=\"api\\nReady\" style=\"filled\" fillcolor=\"#c6f6d5\"];",
		"\"db\" [label=\"db\\nBlocked\\nblocked waiting for storage (started)\" style=\"filled\" fillcolor=\"#fefcbf\"];",
		"\"storage\" [label=\"storage\\nFailed\\nprobe failed\" style=\"filled\" fillcolor=\"#fed7d7\"];",
		"\"api\" -> \"db\" [label=\"require=ready\"];",
		"\"db\" -> \"storage\" [label=\"require=started\\nblocked waiting for storage (started)\"];",
	}

	for _, expected := range expectations {
		if !strings.Contains(dot, expected) {
			t.Fatalf("expected DOT output to contain %q, got:\n%s", expected, dot)
		}
	}
}
