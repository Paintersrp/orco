package cli

import (
	"bytes"
	stdcontext "context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/example/orco/internal/engine"
	"github.com/example/orco/internal/runtime"
	"github.com/example/orco/internal/stack"
)

func TestUpCommandStartsServicesInDependencyOrder(t *testing.T) {
	t.Parallel()

	rt := newMockRuntime()
	rt.logs["db"] = []string{"database online"}

	stackPath := writeStackFile(t, `version: "0.1"
stack:
  name: "demo"
  workdir: "."
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

	cmd := newUpCmd(ctx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("up command failed: %v\nstderr: %s", err, stderr.String())
	}

	expectedStart := []string{"db", "api", "worker"}
	if !reflect.DeepEqual(rt.startOrder(), expectedStart) {
		t.Fatalf("unexpected start order: got %v want %v", rt.startOrder(), expectedStart)
	}
	if !reflect.DeepEqual(rt.readyOrder(), expectedStart) {
		t.Fatalf("unexpected ready order: got %v want %v", rt.readyOrder(), expectedStart)
	}
	expectedStop := []string{"worker", "api", "db"}
	if !reflect.DeepEqual(rt.stopOrder(), expectedStop) {
		t.Fatalf("unexpected stop order: got %v want %v", rt.stopOrder(), expectedStop)
	}

	if !bytes.Contains(stdout.Bytes(), []byte("All services reported ready.")) {
		t.Fatalf("expected readiness message in stdout, got: %s", stdout.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("Services shut down cleanly.")) {
		t.Fatalf("expected shutdown message in stdout, got: %s", stdout.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte("[db] database online")) {
		t.Fatalf("expected log output in stdout, got: %s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr output, got: %s", stderr.String())
	}
}

func TestUpCommandPropagatesRuntimeErrors(t *testing.T) {
	t.Parallel()

	rt := newMockRuntime()
	rt.waitErr["api"] = errors.New("not healthy")

	stackPath := writeStackFile(t, `version: "0.1"
stack:
  name: "demo"
  workdir: "."
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

	cmd := newUpCmd(ctx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected command to fail due to readiness error")
	}

	expectedStart := []string{"db", "api"}
	if !reflect.DeepEqual(rt.startOrder(), expectedStart) {
		t.Fatalf("unexpected start order: got %v want %v", rt.startOrder(), expectedStart)
	}
	if !reflect.DeepEqual(rt.readyOrder(), []string{"db"}) {
		t.Fatalf("unexpected ready order: got %v want %v", rt.readyOrder(), []string{"db"})
	}
	expectedStop := []string{"api", "db"}
	if !reflect.DeepEqual(rt.stopOrder(), expectedStop) {
		t.Fatalf("unexpected stop order: got %v want %v", rt.stopOrder(), expectedStop)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("error: api readiness failed")) {
		t.Fatalf("expected readiness error in stderr, got: %s", stderr.String())
	}
}

func writeStackFile(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write stack file: %v", err)
	}
	return path
}

type mockRuntime struct {
	mu        sync.Mutex
	starts    []string
	readies   []string
	stops     []string
	readyCh   map[string]chan struct{}
	startErr  map[string]error
	waitErr   map[string]error
	stopErr   map[string]error
	logs      map[string][]string
	autoReady bool
}

func newMockRuntime() *mockRuntime {
	return &mockRuntime{
		readyCh:   make(map[string]chan struct{}),
		startErr:  make(map[string]error),
		waitErr:   make(map[string]error),
		stopErr:   make(map[string]error),
		logs:      make(map[string][]string),
		autoReady: true,
	}
}

func (m *mockRuntime) Start(ctx stdcontext.Context, name string, svc *stack.Service) (runtime.Instance, error) {
	m.mu.Lock()
	if err := m.startErr[name]; err != nil {
		m.mu.Unlock()
		return nil, err
	}
	m.starts = append(m.starts, name)
	readyCh := make(chan struct{})
	m.readyCh[name] = readyCh
	waitErr := m.waitErr[name]
	stopErr := m.stopErr[name]
	logLines := append([]string(nil), m.logs[name]...)
	autoReady := m.autoReady
	m.mu.Unlock()

	logsCh := make(chan string, len(logLines))
	for _, line := range logLines {
		logsCh <- line
	}

	inst := &mockInstance{
		runtime: m,
		name:    name,
		ready:   readyCh,
		logs:    logsCh,
		waitErr: waitErr,
		stopErr: stopErr,
	}

	if autoReady {
		go func() {
			time.Sleep(10 * time.Millisecond)
			m.SignalReady(name)
		}()
	}

	return inst, nil
}

func (m *mockRuntime) SignalReady(name string) {
	m.mu.Lock()
	ch, ok := m.readyCh[name]
	if ok {
		delete(m.readyCh, name)
	}
	m.mu.Unlock()
	if ok {
		close(ch)
	}
}

func (m *mockRuntime) recordReady(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readies = append(m.readies, name)
}

func (m *mockRuntime) recordStop(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stops = append(m.stops, name)
}

func (m *mockRuntime) startOrder() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.starts...)
}

func (m *mockRuntime) readyOrder() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.readies...)
}

func (m *mockRuntime) stopOrder() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.stops...)
}

type mockInstance struct {
	runtime  *mockRuntime
	name     string
	ready    chan struct{}
	logs     chan string
	waitErr  error
	stopErr  error
	stopOnce sync.Once
}

func (i *mockInstance) WaitReady(ctx stdcontext.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-i.ready:
		if i.waitErr != nil {
			return i.waitErr
		}
		i.runtime.recordReady(i.name)
		return nil
	}
}

func (i *mockInstance) Stop(ctx stdcontext.Context) error {
	var err error
	i.stopOnce.Do(func() {
		i.runtime.recordStop(i.name)
		close(i.logs)
		err = i.stopErr
	})
	return err
}

func (i *mockInstance) Logs() <-chan string {
	return i.logs
}
