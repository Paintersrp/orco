package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatePortCollisions(t *testing.T) {
	mkService := func(ports ...string) *ServiceSpec {
		return &ServiceSpec{
			Runtime:  "docker",
			Image:    "ghcr.io/demo/app:latest",
			Replicas: 1,
			Ports:    ports,
			Health: &ProbeSpec{
				HTTP: &HTTPProbeSpec{URL: "http://localhost:8080/healthz"},
			},
		}
	}

	cases := []struct {
		name     string
		services map[string]*ServiceSpec
		contains []string
	}{
		{
			name: "conflict on wildcard interface",
			services: map[string]*ServiceSpec{
				"api":    mkService("8080:80"),
				"worker": mkService("8080:80"),
			},
			contains: []string{
				"host port 8080",
				"IP \"0.0.0.0\"",
				"service(s) api, worker",
				"next available port is 8081",
			},
		},
		{
			name: "wildcard conflicts with explicit ip",
			services: map[string]*ServiceSpec{
				"web": mkService("8080:80"),
				"api": mkService("127.0.0.1:8080:80"),
			},
			contains: []string{
				"host port 8080",
				"IP \"127.0.0.1\"",
				"service(s) api, web",
				"next available port is 8081",
			},
		},
		{
			name: "conflict on explicit ip with occupied successor",
			services: map[string]*ServiceSpec{
				"db": mkService("127.0.0.1:8080:80", "127.0.0.1:8081:81"),
				"ui": mkService("127.0.0.1:8080:80"),
			},
			contains: []string{
				"host port 8080",
				"IP \"127.0.0.1\"",
				"service(s) db, ui",
				"next available port is 8082",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stack := &Stack{
				Version:  "0.1",
				Stack:    StackMeta{Name: "demo"},
				Services: tc.services,
			}
			err := stack.Validate()
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			for _, want := range tc.contains {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("expected error to contain %q, got %v", want, err)
				}
			}
		})
	}
}

func TestLoadWarnsOnSharedWritableVolumes(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}

	stackPath := filepath.Join(dir, "stack.yaml")
	manifest := fmt.Sprintf(`
version: "0.1"
stack:
  name: demo
services:
  api:
    runtime: docker
    image: ghcr.io/example/api:latest
    health:
      http:
        url: http://localhost:8080/healthz
    volumes:
      - %s:/var/lib/api
  worker:
    runtime: docker
    image: ghcr.io/example/worker:latest
    health:
      http:
        url: http://localhost:8081/healthz
    volumes:
      - %s:/var/lib/worker
`, dataDir, dataDir)
	if err := os.WriteFile(stackPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	stack, err := Load(stackPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if len(stack.Warnings) == 0 {
		t.Fatalf("expected warnings, got none")
	}
	warning := stack.Warnings[0]
	for _, want := range []string{"api", "worker", dataDir} {
		if !strings.Contains(warning, want) {
			t.Fatalf("warning %q missing %q", warning, want)
		}
	}
}
