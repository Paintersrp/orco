package process

import (
	"context"
	"os"
	"path/filepath"
	stdruntime "runtime"
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
			GracePeriod:      stack.Duration{},
			Interval:         stack.Duration{Duration: 20 * time.Millisecond},
			Timeout:          stack.Duration{Duration: 100 * time.Millisecond},
			FailureThreshold: 50,
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	deployment, err := orch.Up(ctx, doc, graph, nil)
	if err != nil {
		t.Fatalf("orchestrator up: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := deployment.Stop(stopCtx, nil); err != nil {
			t.Logf("deployment stop returned error: %v", err)
		}
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

	dbInfo := waitForFile(readyFile)
	webInfo := waitForFile(startedFile)

	if webInfo.ModTime().Before(dbInfo.ModTime()) {
		t.Fatalf("web started before dependency was ready: web=%v db=%v", webInfo.ModTime(), dbInfo.ModTime())
	}
}
