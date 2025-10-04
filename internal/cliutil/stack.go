package cliutil

import (
	"fmt"
	"path/filepath"

	"github.com/example/orco/internal/config"
	"github.com/example/orco/internal/engine"
)

// StackDocument bundles a parsed stack file with the derived dependency graph.
type StackDocument struct {
	File   *config.Stack
	Graph  *engine.Graph
	Source string
}

// LoadStackFromFile parses a stack definition file and returns its document and graph.
func LoadStackFromFile(path string) (*StackDocument, error) {
	doc, err := config.Load(path)
	if err != nil {
		return nil, err
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve stack path: %w", err)
	}

	graph, err := engine.BuildGraph(doc)
	if err != nil {
		return nil, err
	}
	return &StackDocument{File: doc, Graph: graph, Source: absPath}, nil
}
