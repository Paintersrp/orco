package podman

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/containers/podman/v4/pkg/bindings"
	"github.com/containers/podman/v4/pkg/bindings/system"
	"github.com/containers/podman/v4/pkg/errorhandling"

	"github.com/Paintersrp/orco/internal/config"
	"github.com/Paintersrp/orco/internal/probe"
	"github.com/Paintersrp/orco/internal/runtime"
	"github.com/Paintersrp/orco/internal/runtime/containerutil"
	"github.com/Paintersrp/orco/internal/stack"
)

func requirePodman(t *testing.T) {
	t.Helper()
	conn, err := bindings.NewConnection(context.Background(), "")
	if err != nil {
		t.Skipf("podman connection: %v", err)
	}
	ctx, cancel := context.WithTimeout(conn, 3*time.Second)
	defer cancel()
	if _, err := system.Version(ctx, nil); err != nil {
		t.Skipf("podman version: %v", err)
	}
}

func TestRuntimeStartStopLogs(t *testing.T) {
	requirePodman(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	rt := New()
	spec := runtime.StartSpec{
		Name:    "log-loop",
		Image:   "ghcr.io/library/alpine:3.19",
		Command: []string{"sh", "-c", "while true; do echo orco-ready; sleep 1; done"},
	}

	inst, err := rt.Start(ctx, spec)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		_ = inst.Stop(stopCtx)
	}()

	if err := inst.WaitReady(ctx); err != nil {
		t.Fatalf("wait ready: %v", err)
	}

	logs, err := inst.Logs(ctx)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if logs == nil {
		t.Fatal("logs channel is nil")
	}

	var line runtime.LogEntry
	select {
	case line = <-logs:
	case <-time.After(30 * time.Second):
		t.Fatal("expected log line")
	}
	if line.Message == "" {
		t.Fatal("expected non-empty log line")
	}

	drained := make(chan struct{})
	go func() {
		for range logs {
		}
		close(drained)
	}()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopCancel()
	if err := inst.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	select {
	case <-drained:
	case <-time.After(30 * time.Second):
		t.Fatal("logs channel did not close")
	}
}

func TestRuntimeWaitReadyWithLogExpression(t *testing.T) {
	requirePodman(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	rt := New()

	health := &stack.Health{
		GracePeriod:      stack.Duration{Duration: 200 * time.Millisecond},
		Interval:         stack.Duration{Duration: 50 * time.Millisecond},
		Timeout:          stack.Duration{Duration: 150 * time.Millisecond},
		FailureThreshold: 3,
		SuccessThreshold: 1,
		HTTP: &stack.HTTPProbe{
			URL: "http://127.0.0.1:65535",
		},
		Log: &stack.LogProbe{
			Pattern: "ready",
			Sources: []string{runtime.LogSourceStdout},
		},
		Expression: "http || log",
	}

	spec := runtime.StartSpec{
		Name:    "log-ready",
		Image:   "ghcr.io/library/alpine:3.19",
		Command: []string{"sh", "-c", "echo booting; sleep 0.05; echo ready; sleep 1"},
		Health:  health,
	}

	inst, err := rt.Start(ctx, spec)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		if err := inst.Stop(stopCtx); err != nil {
			t.Logf("stop returned error: %v", err)
		}
	}()

	if err := inst.WaitReady(ctx); err != nil {
		t.Fatalf("wait ready: %v", err)
	}

	healthCh := inst.Health()
	if healthCh == nil {
		t.Fatalf("expected health channel")
	}

	select {
	case evt := <-healthCh:
		if evt.Status != probe.StatusReady {
			t.Fatalf("expected ready event, got %v", evt.Status)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("expected ready event")
	}
}

func TestRuntimeEnvFromFile(t *testing.T) {
	requirePodman(t)

	t.Setenv("PODMAN_FILE_VALUE", "from-env")

	stackDir := t.TempDir()
	envPath := filepath.Join(stackDir, "vars.env")
	if err := os.WriteFile(envPath, []byte("FOO=${PODMAN_FILE_VALUE}\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	manifest := []byte(`version: 0.1
stack:
  name: demo
  workdir: .
services:
  api:
    runtime: podman
    image: ghcr.io/library/alpine:3.19
    command: ["sh", "-c", "echo $FOO && sleep 2"]
    env_files: [vars.env]
`)

	manifestPath := filepath.Join(stackDir, "stack.yaml")
	if err := os.WriteFile(manifestPath, manifest, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	doc, err := config.Load(manifestPath)
	if err != nil {
		t.Fatalf("load stack: %v", err)
	}

	svc := doc.Services["api"]
	if len(svc.Env) != 1 || svc.Env["FOO"] != "from-env" {
		t.Fatalf("expected env to be resolved, got %v", svc.Env)
	}
}

func TestRuntimeWaitExitErrorIncludesOOM(t *testing.T) {
	outcome := waitOutcome{exitCode: 137, oomKilled: true, memoryLimit: "256Mi"}
	err := containerutil.WaitExitError(outcome.toStatus())
	if err == nil {
		t.Fatalf("expected error for oom exit")
	}
	want := "container terminated by the kernel OOM killer (memory limit 256Mi): container exited with status 137"
	if err.Error() != want {
		t.Fatalf("unexpected error message: got %q want %q", err.Error(), want)
	}
}

func TestIsNotFound(t *testing.T) {
	if !isNotFound(&errorhandling.ErrorModel{ResponseCode: http.StatusNotFound}) {
		t.Fatalf("expected isNotFound to return true")
	}
	if isNotFound(nil) {
		t.Fatalf("expected isNotFound to return false for nil")
	}
}
