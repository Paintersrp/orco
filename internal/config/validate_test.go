package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testServiceSpec(ports ...string) *ServiceSpec {
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

func TestValidatePortCollisions(t *testing.T) {
	cases := []struct {
		name          string
		services      map[string]*ServiceSpec
		contains      []string
		containsOneOf []string
	}{
		{
			name: "conflict on wildcard interface",
			services: map[string]*ServiceSpec{
				"api":    testServiceSpec("8080:80"),
				"worker": testServiceSpec("8080:80"),
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
				"web": testServiceSpec("8080:80"),
				"api": testServiceSpec("127.0.0.1:8080:80"),
			},
			contains: []string{
				"host port 8080",
				"service(s) api, web",
				"next available port is 8081",
			},
			containsOneOf: []string{
				"IP \"127.0.0.1\"",
				"IP \"0.0.0.0\"",
			},
		},
		{
			name: "conflict on explicit ip with occupied successor",
			services: map[string]*ServiceSpec{
				"db": testServiceSpec("127.0.0.1:8080:80", "127.0.0.1:8081:81"),
				"ui": testServiceSpec("127.0.0.1:8080:80"),
			},
			contains: []string{
				"host port 8080",
				"IP \"127.0.0.1\"",
				"service(s) db, ui",
				"next available port is 8082",
			},
		},
		{
			name: "ipv6 wildcard conflicts with ipv4 loopback",
			services: map[string]*ServiceSpec{
				"dual":     testServiceSpec("[::]:8080:80"),
				"loopback": testServiceSpec("127.0.0.1:8080:80"),
			},
			contains: []string{
				"host port 8080",
				"service(s) dual, loopback",
				"next available port is 8081",
			},
			containsOneOf: []string{
				"IP \"127.0.0.1\"",
				"IP \"0.0.0.0\"",
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
			if len(tc.containsOneOf) > 0 {
				found := false
				for _, want := range tc.containsOneOf {
					if strings.Contains(err.Error(), want) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("expected error to contain one of %v, got %v", tc.containsOneOf, err)
				}
			}
		})
	}
}

func TestWildcardPortConflictsRegardlessOfOrder(t *testing.T) {
	wildcard := testServiceSpec("0.0.0.0:8080:80")
	specific := testServiceSpec("127.0.0.1:8080:80", "127.0.0.1:8081:81")

	t.Run("specific before wildcard", func(t *testing.T) {
		claimed := map[int]map[string]map[string]*portClaim{}
		if err := claimServicePorts("specific", specific, claimed); err != nil {
			t.Fatalf("claim specific service: %v", err)
		}

		err := claimServicePorts("wildcard", wildcard, claimed)
		if err == nil {
			t.Fatalf("expected wildcard claim to fail, got nil")
		}

		for _, want := range []string{"host port 8080", "IP \"0.0.0.0\"", "service(s) specific, wildcard", "next available port is 8082"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("expected error to contain %q, got %v", want, err)
			}
		}
	})

	t.Run("wildcard before specific", func(t *testing.T) {
		claimed := map[int]map[string]map[string]*portClaim{}
		if err := claimServicePorts("wildcard", wildcard, claimed); err != nil {
			t.Fatalf("claim wildcard service: %v", err)
		}

		err := claimServicePorts("specific", specific, claimed)
		if err == nil {
			t.Fatalf("expected specific claim to fail, got nil")
		}

		for _, want := range []string{"host port 8080", "IP \"127.0.0.1\"", "service(s) specific, wildcard", "next available port is 8081"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("expected error to contain %q, got %v", want, err)
			}
		}
	})
}

func TestPortClaimsAllowDifferentProtocols(t *testing.T) {
	stack := &Stack{
		Version: "0.1",
		Stack:   StackMeta{Name: "demo"},
		Services: map[string]*ServiceSpec{
			"tcp": testServiceSpec("8080:80/tcp"),
			"udp": testServiceSpec("8080:80/udp"),
		},
	}

	if err := stack.Validate(); err != nil {
		t.Fatalf("expected no validation error, got %v", err)
	}
}

func TestNextAvailablePortConsidersProtocols(t *testing.T) {
	claimed := map[int]map[string]map[string]*portClaim{
		8080: {
			"0.0.0.0": {
				"tcp": &portClaim{services: map[string]struct{}{"api": {}}},
			},
		},
	}

	if got := nextAvailablePort("0.0.0.0", "udp", 8079, claimed); got != 8080 {
		t.Fatalf("expected wildcard UDP to use 8080, got %d", got)
	}

	if got := nextAvailablePort("127.0.0.1", "udp", 8079, claimed); got != 8080 {
		t.Fatalf("expected specific UDP to use 8080, got %d", got)
	}

	if got := nextAvailablePort("0.0.0.0", "tcp", 8079, claimed); got != 8081 {
		t.Fatalf("expected wildcard TCP to skip to 8081, got %d", got)
	}
}

func TestNextAvailablePortSkipsIPv6WildcardClaims(t *testing.T) {
	claimed := map[int]map[string]map[string]*portClaim{
		8080: {
			"::": {
				"tcp": &portClaim{services: map[string]struct{}{"dual": {}}},
			},
		},
	}

	if got := nextAvailablePort("0.0.0.0", "tcp", 8079, claimed); got != 8081 {
		t.Fatalf("expected wildcard TCP to skip to 8081 due to IPv6 wildcard, got %d", got)
	}

	if got := nextAvailablePort("127.0.0.1", "tcp", 8079, claimed); got != 8081 {
		t.Fatalf("expected specific TCP to skip to 8081 due to IPv6 wildcard, got %d", got)
	}

	if got := nextAvailablePort("0.0.0.0", "udp", 8079, claimed); got != 8080 {
		t.Fatalf("expected wildcard UDP to reuse 8080 despite IPv6 wildcard TCP, got %d", got)
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
