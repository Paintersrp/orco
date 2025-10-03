package cliutil

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/example/orco/internal/engine"
	"github.com/example/orco/internal/stack"
)

// StackDocument bundles a parsed stack file with the derived dependency graph.
type StackDocument struct {
	File   *stack.StackFile
	Graph  *engine.Graph
	Source string
}

// LoadStackFromFile parses a stack definition file and returns its document and graph.
func LoadStackFromFile(path string) (*StackDocument, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open stack file: %w", err)
	}
	defer f.Close()

	doc, err := stack.Parse(f)
	if err != nil {
		return nil, err
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve stack path: %w", err)
	}
	stackDir := filepath.Dir(absPath)
	resolvedWorkdir := stackDir
	if doc.Stack.Workdir != "" {
		if filepath.IsAbs(doc.Stack.Workdir) {
			resolvedWorkdir = filepath.Clean(doc.Stack.Workdir)
		} else {
			resolvedWorkdir = filepath.Clean(filepath.Join(stackDir, doc.Stack.Workdir))
		}
	}

	for _, svc := range doc.Services {
		if svc == nil {
			continue
		}
		svc.ResolvedWorkdir = resolvedWorkdir
	}

	graph, err := engine.BuildGraph(doc)
	if err != nil {
		return nil, err
	}
	return &StackDocument{File: doc, Graph: graph, Source: path}, nil
}
