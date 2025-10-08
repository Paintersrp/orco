package cli

import (
	stdcontext "context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/api"
	apihttp "github.com/Paintersrp/orco/internal/api/http"
	"github.com/Paintersrp/orco/internal/engine"
	"github.com/Paintersrp/orco/internal/runtime"
	"github.com/Paintersrp/orco/internal/stack"
)

func TestControlAPIStatusRestartAndApply(t *testing.T) {
	t.Parallel()

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
    replicas: 1
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

	events := make(chan engine.Event, 256)
	trackedEvents, release := ctx.trackEvents(doc.File.Stack.Name, events, cap(events))
	defer release()

	drainDone := make(chan struct{})
	go func() {
		for range trackedEvents {
		}
		close(drainDone)
	}()

	runCtx, runCancel := stdcontext.WithCancel(stdcontext.Background())
	defer runCancel()

	deployment, err := ctx.getOrchestrator().Up(runCtx, doc.File, doc.Graph, events)
	if err != nil {
		t.Fatalf("orchestrator up: %v", err)
	}
	ctx.setDeployment(deployment, doc.File.Stack.Name, stack.CloneServiceMap(doc.File.Services))
	defer func() {
		stopCtx, cancel := stdcontext.WithTimeout(stdcontext.Background(), time.Second)
		_ = deployment.Stop(stopCtx, events)
		cancel()
		ctx.clearDeployment(deployment)
		close(events)
		<-drainDone
	}()

	waitForServiceReady(t, ctx.statusTracker(), "api", 1)

	control := NewControlAPI(ctx)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server, err := apihttp.NewServer(apihttp.Config{Controller: control, Listener: listener})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	serverCtx, serverCancel := stdcontext.WithCancel(stdcontext.Background())
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Run(serverCtx)
	}()
	t.Cleanup(func() {
		serverCancel()
		if err := <-serverErr; err != nil {
			t.Fatalf("server error: %v", err)
		}
	})

	baseURL := fmt.Sprintf("http://%s", server.Addr())

	statusResp, err := http.Get(baseURL + "/api/v1/status")
	if err != nil {
		t.Fatalf("status request: %v", err)
	}
	defer statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status code: %d", statusResp.StatusCode)
	}

	var statusBody api.StatusReport
	if err := json.NewDecoder(statusResp.Body).Decode(&statusBody); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if statusBody.Stack != doc.File.Stack.Name {
		t.Fatalf("unexpected stack name: %s", statusBody.Stack)
	}
	apiService, ok := statusBody.Services["api"]
	if !ok {
		t.Fatalf("status missing api service: %+v", statusBody.Services)
	}
	if !apiService.Ready {
		t.Fatalf("expected api ready, got %+v", apiService)
	}
	if len(apiService.History) == 0 {
		t.Fatalf("expected history entries, got none")
	}

	restartResp, err := http.Post(baseURL+"/api/v1/restart/api", "application/json", nil)
	if err != nil {
		t.Fatalf("restart request: %v", err)
	}
	defer restartResp.Body.Close()
	if restartResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected restart status: %d", restartResp.StatusCode)
	}
	var restartBody struct {
		Restart api.RestartResult `json:"restart"`
	}
	if err := json.NewDecoder(restartResp.Body).Decode(&restartBody); err != nil {
		t.Fatalf("decode restart body: %v", err)
	}
	if restartBody.Restart.Service != "api" {
		t.Fatalf("unexpected service in restart result: %+v", restartBody.Restart)
	}

	if restartBody.Restart.Replicas != 1 || restartBody.Restart.ReadyReplicas != 1 {
		t.Fatalf("unexpected replica counts in restart result: %+v", restartBody.Restart)
	}
	if restartBody.Restart.CompletedAt.IsZero() {
		t.Fatalf("expected completion timestamp in restart result")
	}

	history := ctx.statusTracker().History("api", 5)
	var sawRestart bool
	for _, entry := range history {
		if entry.Reason == engine.ReasonRestart {
			sawRestart = true
			break
		}
	}
	if !sawRestart {
		t.Fatalf("expected restart reason in history, got %+v", history)
	}

	applyResp, err := http.Post(baseURL+"/api/v1/apply", "application/json", nil)
	if err != nil {
		t.Fatalf("apply request: %v", err)
	}
	defer applyResp.Body.Close()
	if applyResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected apply status: %d", applyResp.StatusCode)
	}
	var applyBody struct {
		Apply  api.ApplyResult   `json:"apply"`
		Status *api.StatusReport `json:"status"`
	}
	if err := json.NewDecoder(applyResp.Body).Decode(&applyBody); err != nil {
		t.Fatalf("decode apply body: %v", err)
	}
	if applyBody.Apply.Stack != doc.File.Stack.Name {
		t.Fatalf("unexpected stack in apply result: %+v", applyBody.Apply)
	}
	if applyBody.Apply.Diff != "No changes detected." {
		t.Fatalf("unexpected apply diff: %q", applyBody.Apply.Diff)
	}
	if applyBody.Status == nil {
		t.Fatalf("expected status snapshot in apply response")
	}
	if svc, ok := applyBody.Status.Services["api"]; !ok || !svc.Ready {
		t.Fatalf("expected ready api service in apply snapshot, got %+v", applyBody.Status.Services)
	}
}

func waitForServiceReady(t *testing.T, tracker *statusTracker, service string, replicas int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap := tracker.Snapshot()[service]
		if snap.Ready && snap.ReadyReplicas >= replicas {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service %s did not report ready", service)
}
