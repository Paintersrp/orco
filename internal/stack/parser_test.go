package stack

import (
	"strings"
	"testing"
	"time"
)

func TestStackFileValidateAcceptsSupportedRuntimes(t *testing.T) {
	tests := map[string]string{
		"docker":  "docker",
		"process": "process",
	}

	for name, runtime := range tests {
		t.Run(name, func(t *testing.T) {
			sf := StackFile{
				Version: "0.1",
				Stack:   StackMeta{Name: "test"},
				Services: ServiceMap{
					"app": {
						Runtime:  runtime,
						Replicas: 1,
					},
				},
			}

			if err := sf.Validate(); err != nil {
				t.Fatalf("expected runtime %s to be accepted, got error: %v", runtime, err)
			}
		})
	}
}

func TestStackFileValidateRejectsUnsupportedRuntime(t *testing.T) {
	sf := StackFile{
		Version: "0.1",
		Stack:   StackMeta{Name: "test"},
		Services: ServiceMap{
			"app": {
				Runtime:  "unsupported",
				Replicas: 1,
			},
		},
	}

	err := sf.Validate()
	if err == nil {
		t.Fatalf("expected error for unsupported runtime")
	}
}

func TestStackFileValidateErrorsIncludePaths(t *testing.T) {
	baseStack := func() StackFile {
		return StackFile{
			Version: "0.1",
			Stack:   StackMeta{Name: "test"},
			Services: ServiceMap{
				"app": {
					Runtime:  "docker",
					Replicas: 1,
				},
			},
		}
	}

	tests := []struct {
		name   string
		modify func(sf *StackFile)
		want   string
	}{
		{
			name: "missing version",
			modify: func(sf *StackFile) {
				sf.Version = ""
			},
			want: "version",
		},
		{
			name: "missing stack name",
			modify: func(sf *StackFile) {
				sf.Stack.Name = ""
			},
			want: "stack.name",
		},
		{
			name: "missing runtime",
			modify: func(sf *StackFile) {
				sf.Services["app"].Runtime = ""
			},
			want: "services.app.runtime",
		},
		{
			name: "invalid dependency require",
			modify: func(sf *StackFile) {
				sf.Services["db"] = &Service{Runtime: "docker", Replicas: 1}
				sf.Services["app"].DependsOn = []Dependency{{Target: "db", Require: "invalid"}}
			},
			want: "services.app.dependsOn[0].require",
		},
		{
			name: "missing dependency target",
			modify: func(sf *StackFile) {
				sf.Services["app"].DependsOn = []Dependency{{Require: "ready"}}
			},
			want: "services.app.dependsOn[0].target",
		},
		{
			name: "multiple health probes",
			modify: func(sf *StackFile) {
				sf.Services["app"].Health = &Health{HTTP: &HTTPProbe{}, TCP: &TCPProbe{}}
			},
			want: "services.app.health",
		},
		{
			name: "replicas below minimum",
			modify: func(sf *StackFile) {
				sf.Services["app"].Replicas = 0
			},
			want: "services.app.replicas",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sf := baseStack()
			tc.modify(&sf)

			if err := sf.Validate(); err == nil {
				t.Fatalf("expected error")
			} else if got := err.Error(); !strings.Contains(got, tc.want) {
				t.Fatalf("expected error to contain %q, got %q", tc.want, got)
			}
		})
	}
}

func TestValidateHealthProbeRequiredFields(t *testing.T) {
	baseStack := func() StackFile {
		return StackFile{
			Version: "0.1",
			Stack:   StackMeta{Name: "test"},
			Services: ServiceMap{
				"app": {
					Runtime:  "docker",
					Replicas: 1,
				},
			},
		}
	}

	tests := []struct {
		name   string
		health *Health
		want   string
	}{
		{
			name:   "http url required",
			health: &Health{HTTP: &HTTPProbe{}},
			want:   "services.app.health.http.url: is required",
		},
		{
			name:   "tcp address required",
			health: &Health{TCP: &TCPProbe{}},
			want:   "services.app.health.tcp.address: is required",
		},
		{
			name:   "cmd command required",
			health: &Health{Command: &CommandProbe{}},
			want:   "services.app.health.cmd.command: must contain at least one entry",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sf := baseStack()
			sf.Services["app"].Health = tc.health

			err := sf.Validate()
			if err == nil {
				t.Fatalf("expected error")
			}
			if got := err.Error(); got != tc.want {
				t.Fatalf("unexpected error, got %q want %q", got, tc.want)
			}
		})
	}
}

func TestApplyDefaultsMergesHealth(t *testing.T) {
	input := `version: 0.1
stack:
  name: test
defaults:
  health:
    interval: 10s
    timeout: 3s
    gracePeriod: 1m
    failureThreshold: 9
    successThreshold: 4
services:
  api:
    runtime: docker
    replicas: 1
    health:
      http:
        url: http://localhost:8080/health
`

	stack, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse stack: %v", err)
	}

	svc, ok := stack.Services["api"]
	if !ok {
		t.Fatalf("expected api service to be present")
	}

	if svc.Health == nil {
		t.Fatalf("expected health configuration to be set")
	}

	if svc.Health.HTTP == nil {
		t.Fatalf("expected http probe to remain configured")
	}

	if got := svc.Health.Interval.Duration; got != 10*time.Second {
		t.Fatalf("expected interval default of 10s, got %s", got)
	}
	if got := svc.Health.Timeout.Duration; got != 3*time.Second {
		t.Fatalf("expected timeout default of 3s, got %s", got)
	}
	if got := svc.Health.GracePeriod.Duration; got != time.Minute {
		t.Fatalf("expected grace period default of 1m, got %s", got)
	}
	if got := svc.Health.FailureThreshold; got != 9 {
		t.Fatalf("expected failure threshold default of 9, got %d", got)
	}
	if got := svc.Health.SuccessThreshold; got != 4 {
		t.Fatalf("expected success threshold default of 4, got %d", got)
	}
}
