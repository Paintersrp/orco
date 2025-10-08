package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigLintSuccess(t *testing.T) {
	manifest := stackManifest(
		"version: 0.1",
		"stack:",
		"  name: demo",
		"services:",
		"  api:",
		"    image: example/api:latest",
		"    runtime: docker",
		"    health:",
		"      tcp:",
		"        address: localhost:8080",
	)
	stdout, stderr, path, err := runConfigLint(t, manifest)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	want := fmt.Sprintf("%s: OK\n", path)
	if stdout != want {
		t.Fatalf("unexpected stdout: got %q want %q", stdout, want)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr output: %q", stderr)
	}
}

func TestConfigLintSchemaViolation(t *testing.T) {
	manifest := stackManifest(
		"version: 0.1",
		"stack:",
		"  name: demo",
		"services: []",
	)
	stdout, stderr, _, err := runConfigLint(t, manifest)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout, got %q", stdout)
	}
	if !strings.Contains(stderr, "schema validation failed") {
		t.Fatalf("stderr does not mention schema failure: %q", stderr)
	}
	if !strings.Contains(stderr, "services") {
		t.Fatalf("stderr does not mention services path: %q", stderr)
	}
}

func TestConfigLintMissingStackName(t *testing.T) {
	manifest := stackManifest(
		"version: 0.1",
		"stack: {}",
		"services:",
		"  api:",
		"    image: example/api:latest",
		"    runtime: docker",
		"    health:",
		"      tcp:",
		"        address: localhost:8080",
	)
	stdout, stderr, path, err := runConfigLint(t, manifest)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout, got %q", stdout)
	}
	if stderr == "" {
		t.Fatalf("expected stderr output, got empty string")
	}
	if !strings.Contains(stderr, filepath.Base(path)) {
		t.Fatalf("stderr does not mention stack path: %q", stderr)
	}
}

func TestConfigLintDockerRequiresImage(t *testing.T) {
	manifest := stackManifest(
		"version: 0.1",
		"stack:",
		"  name: demo",
		"services:",
		"  api:",
		"    runtime: docker",
		"    health:",
		"      tcp:",
		"        address: localhost:8080",
	)
	stdout, stderr, _, err := runConfigLint(t, manifest)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout, got %q", stdout)
	}
	if !strings.Contains(stderr, "services.api.image") {
		t.Fatalf("stderr does not mention missing image: %q", stderr)
	}
}

func TestConfigLintProcessRequiresCommand(t *testing.T) {
	manifest := stackManifest(
		"version: 0.1",
		"stack:",
		"  name: demo",
		"services:",
		"  worker:",
		"    runtime: process",
		"    health:",
		"      tcp:",
		"        address: localhost:8080",
	)
	stdout, stderr, _, err := runConfigLint(t, manifest)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout, got %q", stdout)
	}
	if !strings.Contains(stderr, "services.worker.command") {
		t.Fatalf("stderr does not mention missing command: %q", stderr)
	}
}

func runConfigLint(t *testing.T, manifest string) (stdout, stderr, path string, err error) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "stack.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	cmd := NewRootCmd()
	outBuf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	cmd.SetOut(outBuf)
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{"config", "lint", "--file", path})

	err = cmd.Execute()
	return outBuf.String(), errBuf.String(), path, err
}

func stackManifest(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}
