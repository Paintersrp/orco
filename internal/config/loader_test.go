package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadValidStack(t *testing.T) {
	dir := t.TempDir()
	workdir := filepath.Join(dir, "app")
	if err := os.Mkdir(workdir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	envFile := filepath.Join(workdir, "vars.env")
	if err := os.WriteFile(envFile, []byte("TOKEN=abc"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	t.Setenv("WORKDIR_PATH", "./app")
	t.Setenv("ENV_FILE", "./vars.env")
	t.Setenv("API_PASSWORD", "s3cr3t")

	stackPath := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
  workdir: ${WORKDIR_PATH}
services:
  api:
    image: ghcr.io/demo/api:latest
    runtime: docker
    env:
      PASSWORD: ${API_PASSWORD}
    envFromFile: ${ENV_FILE}
    ports: ["8080:8080"]
    health:
      http:
        url: http://localhost:8080/health
`)
	if err := os.WriteFile(stackPath, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	doc, err := Load(stackPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got, want := doc.Stack.Workdir, workdir; got != want {
		t.Fatalf("unexpected workdir: got %q want %q", got, want)
	}
	svc := doc.Services["api"]
	if svc == nil {
		t.Fatalf("service api missing")
	}
	if got, want := svc.ResolvedWorkdir, workdir; got != want {
		t.Fatalf("resolved workdir mismatch: got %q want %q", got, want)
	}
	if got, want := svc.Env["PASSWORD"], "s3cr3t"; got != want {
		t.Fatalf("env expansion mismatch: got %q want %q", got, want)
	}
	if got, want := svc.EnvFromFile, envFile; got != want {
		t.Fatalf("envFromFile not resolved: got %q want %q", got, want)
	}
	if got, want := svc.Replicas, 1; got != want {
		t.Fatalf("replicas default mismatch: got %d want %d", got, want)
	}
	if svc.Health == nil {
		t.Fatalf("health probe not loaded")
	}
	if got, want := svc.Health.Interval.Duration, 2*time.Second; got != want {
		t.Fatalf("interval default mismatch: got %v want %v", got, want)
	}
	if got, want := svc.Health.Timeout.Duration, time.Second; got != want {
		t.Fatalf("timeout default mismatch: got %v want %v", got, want)
	}
	if got, want := svc.Health.FailureThreshold, 3; got != want {
		t.Fatalf("failure threshold mismatch: got %d want %d", got, want)
	}
	if got, want := svc.Health.SuccessThreshold, 1; got != want {
		t.Fatalf("success threshold mismatch: got %d want %d", got, want)
	}
}

func TestLoadMissingStackName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack: {}
services:
  api:
    image: test
    runtime: docker
    health:
      tcp:
        address: localhost:1234
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "stack.name") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error does not contain stack path: %v", err)
	}
}

func TestLoadMultipleProbes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
services:
  api:
    image: test
    runtime: docker
    ports: ["8080:8080"]
    health:
      http:
        url: http://localhost:8080/health
      tcp:
        address: localhost:8080
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "multiple probe types") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadMalformedPort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
services:
  api:
    image: test
    runtime: docker
    ports: ["bad"]
    health:
      tcp:
        address: localhost:8080
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "expected host:container") {
		t.Fatalf("unexpected error: %v", err)
	}
}
