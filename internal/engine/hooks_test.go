package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/stack"
)

func TestCommandHookExecutorRunsCommand(t *testing.T) {
	dir := t.TempDir()

	svc := &stack.Service{
		ResolvedWorkdir: dir,
		Env: map[string]string{
			"FOO": "bar",
		},
	}

	hook := &stack.LifecycleHook{Command: []string{"sh", "-c", "pwd; echo \"$FOO\"; >&2 echo warn"}}

	exec := newCommandHookExecutor()
	res := exec.Run(context.Background(), svc, "preStart", hook)
	if res.Err != nil {
		t.Fatalf("hook run returned error: %v", res.Err)
	}

	sawPwd := false
	sawEnv := false
	sawStderr := false
	for _, entry := range res.Logs {
		switch entry.Message {
		case dir:
			if entry.Stream != hookStreamStdout {
				t.Fatalf("expected pwd log to be stdout, got %q", entry.Stream)
			}
			sawPwd = true
		case "bar":
			if entry.Stream != hookStreamStdout {
				t.Fatalf("expected env log to be stdout, got %q", entry.Stream)
			}
			sawEnv = true
		case "warn":
			if entry.Stream != hookStreamStderr {
				t.Fatalf("expected stderr log to be stderr, got %q", entry.Stream)
			}
			sawStderr = true
		}
	}

	if !sawPwd {
		t.Fatalf("pwd output not observed; logs=%v", res.Logs)
	}
	if !sawEnv {
		t.Fatalf("env output not observed; logs=%v", res.Logs)
	}
	if !sawStderr {
		t.Fatalf("stderr output not observed; logs=%v", res.Logs)
	}

	if len(res.Command) != len(hook.Command) {
		t.Fatalf("expected command to be preserved, got %v", res.Command)
	}
	for i, arg := range hook.Command {
		if res.Command[i] != arg {
			t.Fatalf("command argument mismatch at %d: got %q want %q", i, res.Command[i], arg)
		}
	}
}

func TestCommandHookExecutorTimeout(t *testing.T) {
	hook := &stack.LifecycleHook{
		Command: []string{"sh", "-c", "sleep 1"},
		Timeout: stack.Duration{Duration: 10 * time.Millisecond},
	}

	exec := newCommandHookExecutor()
	start := time.Now()
	res := exec.Run(context.Background(), &stack.Service{}, "preStop", hook)

	if !res.TimedOut {
		t.Fatalf("expected timeout, got %+v", res)
	}
	if !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded error, got %v", res.Err)
	}

	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("hook execution exceeded expected timeout window: %v", elapsed)
	}
}
