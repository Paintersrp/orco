package cli

import (
	"fmt"
	"os"

	"github.com/example/orco/internal/engine"
	"github.com/example/orco/internal/stack"
)

type stackDocument struct {
	File   *stack.StackFile
	Graph  *engine.Graph
	Source string
}

func loadStackFromFile(path string) (*stackDocument, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open stack file: %w", err)
	}
	defer f.Close()

	doc, err := stack.Parse(f)
	if err != nil {
		return nil, err
	}
	graph, err := engine.BuildGraph(doc)
	if err != nil {
		return nil, err
	}
	return &stackDocument{File: doc, Graph: graph, Source: path}, nil
}
