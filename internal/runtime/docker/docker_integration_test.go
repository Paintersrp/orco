package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/client"

	"github.com/example/orco/internal/probe"
	"github.com/example/orco/internal/runtime"
	"github.com/example/orco/internal/stack"
)

func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func requireDocker(t *testing.T) {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("docker client: %v", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		t.Skipf("docker ping: %v", err)
	}
}

func TestRuntimeStartStopLogs(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	rt := New()
	svc := &stack.Service{
		Image:   "ghcr.io/library/alpine:3.19",
		Command: []string{"sh", "-c", "while true; do echo orco-ready; sleep 1; done"},
	}

	spec := runtime.StartSpec{
		Name:    "log-loop",
		Image:   svc.Image,
		Command: svc.Command,
		Env:     svc.Env,
		Ports:   svc.Ports,
		Health:  svc.Health,
		Service: svc,
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

func TestRuntimeHTTPHealth(t *testing.T) {
	requireDocker(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	rt := New()
	svc := &stack.Service{
		Image: "ghcr.io/library/nginx:1.27-alpine",
		Ports: []string{fmt.Sprintf("127.0.0.1:%d:80/tcp", port)},
		Health: &stack.Health{
			Interval: stack.Duration{Duration: 200 * time.Millisecond},
			HTTP: &stack.HTTPProbe{
				URL: fmt.Sprintf("http://127.0.0.1:%d", port),
			},
		},
	}

	spec := runtime.StartSpec{
		Name:    "nginx",
		Image:   svc.Image,
		Command: svc.Command,
		Env:     svc.Env,
		Ports:   svc.Ports,
		Health:  svc.Health,
		Service: svc,
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

	health := inst.Health()
	if health == nil {
		t.Fatal("expected health channel")
	}

	select {
	case state, ok := <-health:
		if !ok {
			t.Fatal("health channel closed early")
		}
		if state.Status != probe.StatusReady {
			t.Fatalf("expected ready state, got %v", state.Status)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("expected health ready state")
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("http get: %v", err)
	}
	resp.Body.Close()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopCancel()
	if err := inst.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestRuntimeContainerExitSurfaced(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	rt := New()
	svc := &stack.Service{
		Image:   "ghcr.io/library/alpine:3.19",
		Command: []string{"sh", "-c", "exit 2"},
	}

	spec := runtime.StartSpec{
		Name:    "exit",
		Image:   svc.Image,
		Command: svc.Command,
		Env:     svc.Env,
		Ports:   svc.Ports,
		Health:  svc.Health,
		Service: svc,
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

	if err := inst.WaitReady(ctx); err == nil {
		t.Fatal("expected wait ready error")
	}
}

func TestRuntimeKillContextCancel(t *testing.T) {
	requireDocker(t)

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const image = "ghcr.io/library/alpine:3.19"
	if err := ensureImage(ctx, cli, image); err != nil {
		t.Fatalf("ensure image: %v", err)
	}

	config := &container.Config{
		Image: image,
		Cmd:   strslice.StrSlice([]string{"sh", "-c", "while true; do sleep 1; done"}),
	}

	createResp, err := cli.ContainerCreate(ctx, config, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("container create: %v", err)
	}
	containerID := createResp.ID
	defer func() {
		removeCtx, removeCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer removeCancel()
		_ = cli.ContainerRemove(removeCtx, containerID, types.ContainerRemoveOptions{Force: true})
	}()

	if err := cli.ContainerStart(ctx, containerID, types.ContainerStartOptions{}); err != nil {
		t.Fatalf("container start: %v", err)
	}

	inst := &dockerInstance{
		cli:         cli,
		containerID: containerID,
		logs:        nil,
		logDone:     closedChan(),
		healthDone:  closedChan(),
		waitDone:    make(chan struct{}),
	}

	go func() {
		time.Sleep(500 * time.Millisecond)
		inst.waitResult = waitOutcome{status: container.WaitResponse{StatusCode: 0}}
		close(inst.waitDone)
	}()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer stopCancel()

	err = inst.Kill(stopCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}

	select {
	case <-inst.waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("waitDone not closed")
	}
}
