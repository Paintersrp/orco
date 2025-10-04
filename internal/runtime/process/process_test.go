package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/example/orco/internal/engine"
	runtimelib "github.com/example/orco/internal/runtime"
	"github.com/example/orco/internal/stack"
)

func TestWaitReadyBlocksUntilProbeSuccess(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("process runtime tests skipped on windows")
	}

	tempDir := t.TempDir()
	readyFile := filepath.Join(tempDir, "db-ready")
	startedFile := filepath.Join(tempDir, "web-started")

	dbService := &stack.Service{
		Runtime: "process",
		Command: []string{"/bin/sh", "-c", "sleep 0.3; touch " + readyFile + "; sleep 1"},
		Health: &stack.Health{
			GracePeriod:      stack.Duration{Duration: 350 * time.Millisecond},
			Interval:         stack.Duration{Duration: 20 * time.Millisecond},
			Timeout:          stack.Duration{Duration: 100 * time.Millisecond},
			FailureThreshold: 2,
			SuccessThreshold: 1,
			Command: &stack.CommandProbe{
				Command: []string{"/bin/sh", "-c", "test -f " + readyFile},
			},
		},
	}

	webService := &stack.Service{
		Runtime: "process",
		DependsOn: []stack.Dependency{{
			Target: "db",
		}},
		Command: []string{"/bin/sh", "-c", "touch " + startedFile + "; sleep 0.1"},
	}

	doc := &stack.StackFile{
		Services: map[string]*stack.Service{
			"db":  dbService,
			"web": webService,
		},
	}

	graph, err := engine.BuildGraph(doc)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}

	orch := engine.NewOrchestrator(runtimelib.Registry{"process": New()})

	events := make(chan engine.Event, 64)
	var (
		mu       sync.Mutex
		recorded []engine.Event
	)
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		for evt := range events {
			mu.Lock()
			recorded = append(recorded, evt)
			mu.Unlock()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	deployment, err := orch.Up(ctx, doc, graph, events)
	if err != nil {
		close(events)
		<-collectorDone
		t.Fatalf("orchestrator up: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := deployment.Stop(stopCtx, events); err != nil {
			t.Logf("deployment stop returned error: %v", err)
		}
		close(events)
		<-collectorDone
	})

	waitForFile := func(path string) os.FileInfo {
		deadline := time.Now().Add(2 * time.Second)
		for {
			info, err := os.Stat(path)
			if err == nil {
				return info
			}
			if !os.IsNotExist(err) {
				t.Fatalf("stat %s: %v", path, err)
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", path)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	nextEventIdx := 0
	waitForEvent := func(predicate func(engine.Event) bool, timeout time.Duration) engine.Event {
		deadline := time.After(timeout)
		for {
			mu.Lock()
			if nextEventIdx < len(recorded) {
				evt := recorded[nextEventIdx]
				nextEventIdx++
				mu.Unlock()
				if predicate(evt) {
					return evt
				}
				continue
			}
			mu.Unlock()
			select {
			case <-deadline:
				t.Fatalf("timed out waiting for event")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}

	dbReady := waitForEvent(func(evt engine.Event) bool {
		return evt.Service == "db" && evt.Type == engine.EventTypeReady
	}, 2*time.Second)
	if dbReady.Err != nil {
		t.Fatalf("db ready event returned error: %v", dbReady.Err)
	}

	webReady := waitForEvent(func(evt engine.Event) bool {
		return evt.Service == "web" && evt.Type == engine.EventTypeReady
	}, 2*time.Second)
	if webReady.Err != nil {
		t.Fatalf("web ready event returned error: %v", webReady.Err)
	}

	dbInfo := waitForFile(readyFile)
	webInfo := waitForFile(startedFile)

	if webInfo.ModTime().Before(dbInfo.ModTime()) {
		t.Fatalf("web started before dependency was ready: web=%v db=%v", webInfo.ModTime(), dbInfo.ModTime())
	}

	if err := os.Remove(readyFile); err != nil {
		t.Fatalf("remove ready file: %v", err)
	}

	dbUnready := waitForEvent(func(evt engine.Event) bool {
		return evt.Service == "db" && evt.Type == engine.EventTypeUnready
	}, 2*time.Second)
	if dbUnready.Err == nil {
		t.Fatalf("expected unready event to include probe error")
	}

	if err := os.WriteFile(readyFile, []byte{}, 0o644); err != nil {
		t.Fatalf("recreate ready file: %v", err)
	}

	dbReadyAgain := waitForEvent(func(evt engine.Event) bool {
		return evt.Service == "db" && evt.Type == engine.EventTypeReady
	}, 2*time.Second)
	if dbReadyAgain.Err != nil {
		t.Fatalf("db re-ready event returned error: %v", dbReadyAgain.Err)
	}
}

func TestStartPreservesBaseEnvironment(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("process runtime tests skipped on windows")
	}

	expectedPath := os.Getenv("PATH")
	if expectedPath == "" {
		t.Skip("PATH is unset in test environment")
	}

	svc := &stack.Service{
		Runtime: "process",
		Command: []string{"/bin/sh", "-c", "echo $PATH"},
	}

	spec := runtimelib.StartSpec{
		Name:    "env-check",
		Command: svc.Command,
		Env:     svc.Env,
		Workdir: svc.ResolvedWorkdir,
		Health:  svc.Health,
		Service: svc,
	}

	inst, err := New().Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("start service: %v", err)
	}
	if _, ok := inst.(*processInstance); !ok {
		t.Fatalf("expected *processInstance, got %T", inst)
	}

	logs, err := inst.Logs(context.Background())
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if logs == nil {
		t.Fatalf("logs channel is nil")
	}

	select {
	case entry, ok := <-logs:
		if !ok {
			t.Fatalf("logs channel closed before emitting output")
		}
		if entry.Message != expectedPath {
			t.Fatalf("PATH mismatch: got %q want %q", entry.Message, expectedPath)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for log output")
	}

	{
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
		waitErr := inst.Wait(waitCtx)
		waitCancel()
		if waitErr != nil {
			t.Fatalf("process exited with error: %v", waitErr)
		}
	}
}

func TestStartSetsWorkingDirectory(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("process runtime tests skipped on windows")
	}

	stackDir := t.TempDir()
	workdir := filepath.Join(stackDir, "workdir")
	if err := os.Mkdir(workdir, 0o755); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	outputPath := filepath.Join(workdir, "pwd.txt")

	svc := &stack.Service{
		Runtime:         "process",
		Command:         []string{"/bin/sh", "-c", "pwd > pwd.txt"},
		ResolvedWorkdir: workdir,
	}

	spec := runtimelib.StartSpec{
		Name:    "workdir-check",
		Command: svc.Command,
		Env:     svc.Env,
		Workdir: svc.ResolvedWorkdir,
		Health:  svc.Health,
		Service: svc,
	}

	inst, err := New().Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("start service: %v", err)
	}
	if _, ok := inst.(*processInstance); !ok {
		t.Fatalf("expected *processInstance, got %T", inst)
	}

	{
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
		waitErr := inst.Wait(waitCtx)
		waitCancel()
		if waitErr != nil {
			t.Fatalf("process exited with error: %v", waitErr)
		}
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read observed working directory: %v", err)
	}
	observed := strings.TrimSpace(string(data))
	if observed != workdir {
		t.Fatalf("working directory mismatch: got %q want %q", observed, workdir)
	}
}

func TestStopTerminatesProcessGroup(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("process runtime tests skipped on windows")
	}

	tempDir := t.TempDir()
	parentPIDPath := filepath.Join(tempDir, "parent.pid")
	childPIDPath := filepath.Join(tempDir, "child.pid")
	scriptPath := filepath.Join(tempDir, "parent.sh")

	script := "#!/bin/sh\n" +
		"set -e\n" +
		"parent_pid_file=\"$1\"\n" +
		"child_pid_file=\"$2\"\n" +
		"echo $$ > \"$parent_pid_file\"\n" +
		"( sleep 60 ) &\n" +
		"child=$!\n" +
		"echo $child > \"$child_pid_file\"\n" +
		"wait\n"

	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write parent script: %v", err)
	}

	svc := &stack.Service{
		Runtime: "process",
		Command: []string{scriptPath, parentPIDPath, childPIDPath},
	}

	spec := runtimelib.StartSpec{
		Name:    "process-group",
		Command: svc.Command,
		Env:     svc.Env,
		Workdir: svc.ResolvedWorkdir,
		Health:  svc.Health,
		Service: svc,
	}

	inst, err := New().Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("start service: %v", err)
	}
	procInst, ok := inst.(*processInstance)
	if !ok {
		t.Fatalf("expected *processInstance, got %T", inst)
	}

	waitForPID := func(path string) int {
		deadline := time.Now().Add(5 * time.Second)
		for {
			data, err := os.ReadFile(path)
			if err == nil {
				pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
				if convErr != nil {
					t.Fatalf("parse pid from %s: %v", path, convErr)
				}
				return pid
			}
			if !os.IsNotExist(err) {
				t.Fatalf("read pid file %s: %v", path, err)
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", path)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	parentPID := waitForPID(parentPIDPath)
	childPID := waitForPID(childPIDPath)

	if err := syscall.Kill(parentPID, 0); err != nil {
		t.Fatalf("parent process not running: %v", err)
	}
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("child process not running: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := procInst.Stop(stopCtx); err != nil {
		t.Logf("process stop returned error: %v", err)
	}

	waitForExit := func(pid int) {
		deadline := time.Now().Add(5 * time.Second)
		for {
			running := true
			if err := syscall.Kill(pid, 0); err != nil {
				if errors.Is(err, syscall.ESRCH) {
					running = false
				} else {
					t.Fatalf("check pid %d: %v", pid, err)
				}
			} else {
				statusPath := fmt.Sprintf("/proc/%d/stat", pid)
				if data, err := os.ReadFile(statusPath); err == nil {
					fields := strings.Fields(string(data))
					if len(fields) > 2 && fields[2] == "Z" {
						running = false
					}
				} else if os.IsNotExist(err) {
					running = false
				} else {
					t.Fatalf("read status for pid %d: %v", pid, err)
				}
			}

			if !running {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for pid %d to exit", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	waitForExit(parentPID)
	waitForExit(childPID)
}
