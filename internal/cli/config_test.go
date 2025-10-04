package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigLintSuccess(t *testing.T) {
	dir := t.TempDir()
	manifest := []byte(`version: 0.1
stack:
  name: demo
services:
  api:
    image: example/api:latest
    runtime: docker
    health:
      tcp:
        address: localhost:8080
`)
	path := filepath.Join(dir, "stack.yaml")
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	cmd := NewRootCmd()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"config", "lint", "--file", path})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr output: %q", stderr.String())
	}
}

func TestConfigLintFailure(t *testing.T) {
	dir := t.TempDir()
	manifest := []byte(`version: 0.1
stack: {}
services:
  api:
    image: example/api:latest
    runtime: docker
    health:
      tcp:
        address: localhost:8080
`)
	path := filepath.Join(dir, "stack.yaml")
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	cmd := NewRootCmd()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"config", "lint", "--file", path})

	if err := cmd.Execute(); err == nil {
		t.Fatalf("expected error, got nil")
	}

	if got := stderr.String(); got == "" {
		t.Fatalf("expected stderr output, got empty string")
	} else if !strings.Contains(got, "stack.yaml") {
		t.Fatalf("stderr does not mention stack path: %q", got)
	}
}
