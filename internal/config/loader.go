package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Load reads a stack manifest from the provided path.
func Load(path string) (*Stack, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve stack path: %w", err)
	}

	f, err := os.Open(absPath)
	if err != nil {
		return nil, fmt.Errorf("open stack file: %w", err)
	}
	defer f.Close()

	decoder := yaml.NewDecoder(f)
	decoder.KnownFields(true)
	var doc Stack
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: decode: %w", absPath, err)
	}

	stackDir := filepath.Dir(absPath)
	resolvedWorkdir := resolveWorkdir(stackDir, os.ExpandEnv(doc.Stack.Workdir))
	doc.Stack.Workdir = resolvedWorkdir

	for _, svc := range doc.Services {
		if svc == nil {
			continue
		}
		svc.ResolvedWorkdir = resolvedWorkdir
		if svc.Env != nil {
			for k, v := range svc.Env {
				svc.Env[k] = os.ExpandEnv(v)
			}
		}
		if svc.EnvFromFile != "" {
			expanded := os.ExpandEnv(svc.EnvFromFile)
			if !filepath.IsAbs(expanded) {
				expanded = filepath.Clean(filepath.Join(resolvedWorkdir, expanded))
			}
			svc.EnvFromFile = expanded
		}
	}

	if err := doc.ApplyDefaults(); err != nil {
		return nil, fmt.Errorf("%s: %w", absPath, err)
	}
	if err := doc.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", absPath, err)
	}
	return &doc, nil
}

func resolveWorkdir(base, workdir string) string {
	if workdir == "" {
		return base
	}
	if filepath.IsAbs(workdir) {
		return filepath.Clean(workdir)
	}
	return filepath.Clean(filepath.Join(base, workdir))
}
