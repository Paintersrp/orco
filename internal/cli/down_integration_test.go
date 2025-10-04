package cli

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/Paintersrp/orco/internal/engine"
	"github.com/Paintersrp/orco/internal/runtime"
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

	cmd := newDownCmd(ctx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected down command to fail due to stop error")
	}
	if got, want := err.Error(), "stop service api: boom"; got != want {
		t.Fatalf("unexpected error: got %q want %q", got, want)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("stop service api: boom")) {
		t.Fatalf("expected stop error in stderr, got: %s", stderr.String())
	}
}
