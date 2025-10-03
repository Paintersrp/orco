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
