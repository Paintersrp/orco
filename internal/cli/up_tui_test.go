package cli

import (
	stdcontext "context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Paintersrp/orco/internal/engine"
	"github.com/Paintersrp/orco/internal/runtime"
)

func TestRunStackTUITracksReadiness(t *testing.T) {
	originalNewUI := newUI
	newUI = func() stackUI {
		return newStubUI()
	}
	defer func() {
		newUI = originalNewUI
	}()

	rt := newMockRuntime()

	stackPath := writeStackFile(t, `version: "0.1"
stack:
  name: "demo"
  workdir: "."
defaults:
  health:
    cmd:
      command: ["true"]
services:
  api:
    runtime: process
    command: ["sleep", "0"]
`)

	ctx := &context{
		stackFile:    &stackPath,
		orchestrator: engine.NewOrchestrator(runtime.Registry{"process": rt}),
	}

	doc, err := ctx.loadStack()
	if err != nil {
		t.Fatalf("load stack: %v", err)
	}
	if services := doc.Graph.Services(); len(services) == 0 {
		t.Fatalf("expected services in graph, got none")
	} else {
		t.Logf("graph services: %v", services)
	}
	if _, ok := doc.File.Services["api"]; !ok {
		t.Fatalf("api service missing from stack")
	}

	cmd := &cobra.Command{Use: "up"}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	cmdCtx, cancel := stdcontext.WithCancel(stdcontext.Background())
	defer cancel()
	cmd.SetContext(cmdCtx)
	if cmd.Context() == nil {
		t.Fatalf("command context is nil")
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runStackTUI(cmd, ctx, doc)
	}()

	tracker := ctx.statusTracker()

	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()

	serviceName := "api"
	readyObserved := false
	for {
		select {
		case <-timeout.C:
			t.Fatalf("timeout waiting for readiness event, snapshot: %#v, starts: %v", tracker.Snapshot(), rt.startOrder())
		case err := <-errCh:
			if err != nil {
				t.Fatalf("runStackTUI returned error: %v", err)
			}
			t.Fatalf("runStackTUI exited before readiness")
		default:
			snapshot := tracker.Snapshot()
			status, ok := snapshot[serviceName]
			if ok && status.State == engine.EventTypeReady && status.Ready {
				readyObserved = true
				goto ready
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

ready:
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runStackTUI returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for runStackTUI to exit")
	}

	if !readyObserved {
		t.Fatalf("ready event not observed")
	}

	snapshot := tracker.Snapshot()
	if _, ok := snapshot[serviceName]; !ok {
		t.Fatalf("service %q not tracked", serviceName)
	}
}

type stubUI struct {
	events    chan engine.Event
	done      chan struct{}
	stopOnce  sync.Once
	closeOnce sync.Once
}

func newStubUI() *stubUI {
	return &stubUI{
		events: make(chan engine.Event, 256),
		done:   make(chan struct{}),
	}
}

func (s *stubUI) Run(ctx stdcontext.Context) error {
	<-ctx.Done()
	s.Stop()
	return nil
}

func (s *stubUI) EventSink() chan<- engine.Event {
	return s.events
}

func (s *stubUI) CloseEvents() {
	s.closeOnce.Do(func() {
		close(s.events)
	})
}

func (s *stubUI) Stop() {
	s.stopOnce.Do(func() {
		close(s.done)
	})
}

func (s *stubUI) Done() <-chan struct{} {
	return s.done
}
