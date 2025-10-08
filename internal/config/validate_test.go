package config

import (
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
