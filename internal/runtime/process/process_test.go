package process

import (
	"context"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"sync"
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
