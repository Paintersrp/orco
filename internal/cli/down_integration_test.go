package cli

import (
	"bytes"
	stdcontext "context"
	"errors"
	"reflect"
	"testing"

	"github.com/Paintersrp/orco/internal/engine"
	"github.com/Paintersrp/orco/internal/runtime"
	"github.com/Paintersrp/orco/internal/stack"
)

func TestDownCommandStopsServicesInReverseOrder(t *testing.T) {
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
  db:
    runtime: process
    command: ["sleep", "0"]
  api:
    runtime: process
    command: ["sleep", "0"]
    dependsOn:
      - target: db
  worker:
    runtime: process
    command: ["sleep", "0"]
    dependsOn:
      - target: api
`)

	ctx := &context{
		stackFile:    &stackPath,
		orchestrator: engine.NewOrchestrator(runtime.Registry{"process": rt}),
	}

	doc, err := ctx.loadStack()
	if err != nil {
		t.Fatalf("load stack: %v", err)
	}
	events := make(chan engine.Event, 64)
	go func() {
		for range events {
		}
	}()
	deployment, err := ctx.getOrchestrator().Up(stdcontext.Background(), doc.File, doc.Graph, events)
	if err != nil {
		t.Fatalf("orchestrator up: %v", err)
	}
	ctx.setDeployment(deployment, doc.File.Stack.Name, stack.CloneServiceMap(doc.File.Services))

	cmd := newDownCmd(ctx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("down command failed: %v\nstderr: %s", err, stderr.String())
	}

	expected := []string{"worker", "api", "db"}
	if got := rt.stopOrder(); !reflect.DeepEqual(got, expected) {
		t.Fatalf("unexpected stop order: got %v want %v", got, expected)
	}

	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr output, got: %s", stderr.String())
	}
}

func TestDownCommandReportsStopErrors(t *testing.T) {
	t.Parallel()

	rt := newMockRuntime()
	rt.stopErr["api"] = errors.New("boom")

	stackPath := writeStackFile(t, `version: "0.1"
stack:
  name: "demo"
  workdir: "."
defaults:
  health:
    cmd:
      command: ["true"]
services:
  db:
    runtime: process
    command: ["sleep", "0"]
  api:
    runtime: process
    command: ["sleep", "0"]
    dependsOn:
      - target: db
`)

	ctx := &context{
		stackFile:    &stackPath,
		orchestrator: engine.NewOrchestrator(runtime.Registry{"process": rt}),
	}

	doc, err := ctx.loadStack()
	if err != nil {
		t.Fatalf("load stack: %v", err)
	}
	events := make(chan engine.Event, 64)
	go func() {
		for range events {
		}
	}()
	deployment, err := ctx.getOrchestrator().Up(stdcontext.Background(), doc.File, doc.Graph, events)
	if err != nil {
		t.Fatalf("orchestrator up: %v", err)
	}
	ctx.setDeployment(deployment, doc.File.Stack.Name, stack.CloneServiceMap(doc.File.Services))
	rt.stopErr["api"] = errors.New("boom")

	cmd := newDownCmd(ctx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err = cmd.Execute()
	if err == nil {
		t.Fatalf("expected down command to fail due to stop error")
	}
	if got, want := err.Error(), "stop service api replica 0: boom"; got != want {
		t.Fatalf("unexpected error: got %q want %q", got, want)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("stop service api replica 0: boom")) {
		t.Fatalf("expected stop error in stderr, got: %s", stderr.String())
	}
}

func TestDownCommandDoesNotStartServices(t *testing.T) {
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
  db:
    runtime: process
    command: ["sleep", "0"]
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
	events := make(chan engine.Event, 64)
	go func() {
		for range events {
		}
	}()
	deployment, err := ctx.getOrchestrator().Up(stdcontext.Background(), doc.File, doc.Graph, events)
	if err != nil {
		t.Fatalf("orchestrator up: %v", err)
	}
	ctx.setDeployment(deployment, doc.File.Stack.Name, stack.CloneServiceMap(doc.File.Services))

	initialStarts := rt.startOrder()
	rt.startErr["db"] = errors.New("should not start")
	rt.startErr["api"] = errors.New("should not start")

	cmd := newDownCmd(ctx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("down command failed: %v\nstderr: %s", err, stderr.String())
	}

	if got := rt.startOrder(); !reflect.DeepEqual(got, initialStarts) {
		t.Fatalf("down command attempted to start services: got %v want %v", got, initialStarts)
	}
}
