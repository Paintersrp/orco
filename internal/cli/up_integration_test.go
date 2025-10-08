package cli

import (
	"bytes"
	stdcontext "context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/config"
	"github.com/Paintersrp/orco/internal/engine"
	"github.com/Paintersrp/orco/internal/probe"
	"github.com/Paintersrp/orco/internal/runtime"
	proxyruntime "github.com/Paintersrp/orco/internal/runtime/proxy"
)

func TestUpCommandStartsServicesInDependencyOrder(t *testing.T) {
	t.Parallel()

	rt := newMockRuntime()
	rt.logs["db"] = []runtime.LogEntry{{Message: "database online", Source: runtime.LogSourceStdout}}

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

	cmd := newUpCmd(ctx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	runCtx, cancel := stdcontext.WithCancel(stdcontext.Background())
	defer cancel()
	cmd.SetContext(runCtx)

	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

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
	if !bytes.Contains(stdout.Bytes(), []byte("\"service\":\"db\"")) ||
		!bytes.Contains(stdout.Bytes(), []byte("\"msg\":\"database online\"")) {
		t.Fatalf("expected structured log output in stdout, got: %s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected no stderr output, got: %s", stderr.String())
	}
}

func TestUpCommandStopsDeploymentBeforeExitOnCancel(t *testing.T) {
	t.Parallel()

	stopRelease := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(stopRelease)
		})
	}
	t.Cleanup(release)

	rt := newBlockingRuntime(stopRelease)

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

	cmd := newUpCmd(ctx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	runCtx, cancel := stdcontext.WithCancel(stdcontext.Background())
	defer cancel()
	cmd.SetContext(runCtx)

	errCh := make(chan error, 1)
	go func() {
		errCh <- cmd.Execute()
	}()

	select {
	case <-rt.readyCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timeout waiting for service readiness")
	}

	time.Sleep(20 * time.Millisecond)

	cancel()

	select {
	case <-rt.stopStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("expected deployment stop to begin after cancellation")
	}

	select {
	case err := <-errCh:
		t.Fatalf("command exited before stop completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	release()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("up command failed: %v\nstderr: %s", err, stderr.String())
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("command did not exit after allowing stop to finish")
	}

	if !bytes.Contains(stdout.Bytes(), []byte("Services shut down cleanly.")) {
		t.Fatalf("expected shutdown message in stdout, got: %s", stdout.String())
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

	cmd := newUpCmd(ctx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected command to fail due to readiness error")
	}

	starts := rt.startOrder()
	if len(starts) == 0 || starts[0] != "db" {
		t.Fatalf("unexpected start order: got %v", starts)
	}
	for _, svc := range starts[1:] {
		if svc != "api" {
			t.Fatalf("unexpected start sequence: got %v", starts)
		}
	}
	if !reflect.DeepEqual(rt.readyOrder(), []string{"db"}) {
		t.Fatalf("unexpected ready order: got %v want %v", rt.readyOrder(), []string{"db"})
	}
	stops := rt.stopOrder()
	if len(stops) == 0 || stops[len(stops)-1] != "db" {
		t.Fatalf("unexpected stop order: got %v", stops)
	}
	for _, svc := range stops[:len(stops)-1] {
		if svc != "api" {
			t.Fatalf("unexpected stop sequence: got %v", stops)
		}
	}
	if !bytes.Contains(stderr.Bytes(), []byte("error: api readiness failed")) &&
		!bytes.Contains(stderr.Bytes(), []byte("service api failed readiness")) {
		t.Fatalf("expected readiness error in stderr, got: %s", stderr.String())
	}
}

func TestUpCommandRoutesTrafficThroughProxy(t *testing.T) {
	stablePort := allocatePort(t)
	canaryPort := allocatePort(t)
	proxyPort := allocatePort(t)

	stackPath := writeStackFile(t, fmt.Sprintf(`version: "0.1"
stack:
  name: "proxy-demo"
  workdir: "."
proxy:
  routes:
    - headers:
        X-Canary: "1"
      pathPrefix: /api/
      service: canary
      port: %d
      stripPathPrefix: true
    - pathPrefix: /api/
      service: stable
      port: %d
      stripPathPrefix: true
services:
  stable:
    runtime: process
    command: ["ignored"]
    env:
      LISTEN_ADDR: 127.0.0.1:%d
      RESPONSE: stable
    health:
      cmd:
        command: ["true"]
  canary:
    runtime: process
    command: ["ignored"]
    env:
      LISTEN_ADDR: 127.0.0.1:%d
      RESPONSE: canary
    update:
      strategy: canary
    health:
      cmd:
        command: ["true"]
`, canaryPort, stablePort, stablePort, canaryPort))

	t.Setenv("ORCO_PROXY_LISTEN", fmt.Sprintf("127.0.0.1:%d", proxyPort))

	doc, err := config.Load(stackPath)
	if err != nil {
		t.Fatalf("load stack: %v", err)
	}
	graph, err := engine.BuildGraph(doc)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}

	events := make(chan engine.Event, 64)
	var drain sync.WaitGroup
	drain.Add(1)
	go func() {
		defer drain.Done()
		for range events {
		}
	}()

	orch := engine.NewOrchestrator(runtime.Registry{
		"process":                newHTTPRuntime(),
		proxyruntime.RuntimeName: proxyruntime.New(),
	})

	deployment, err := orch.Up(stdcontext.Background(), doc, graph, events)
	if err != nil {
		t.Fatalf("up failed: %v", err)
	}

	client := &http.Client{Timeout: 200 * time.Millisecond}
	targetURL := fmt.Sprintf("http://127.0.0.1:%d/api/hello", proxyPort)
	deadline := time.Now().Add(5 * time.Second)

	var stableResp, canaryResp string
	for stableResp == "" || canaryResp == "" {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for proxy responses: stable=%q canary=%q", stableResp, canaryResp)
		}
		if stableResp == "" {
			req, err := http.NewRequest(http.MethodGet, targetURL, nil)
			if err != nil {
				t.Fatalf("construct request: %v", err)
			}
			if body, err := issueRequest(client, req); err == nil {
				stableResp = body
			}
		}
		if canaryResp == "" {
			req, err := http.NewRequest(http.MethodGet, targetURL, nil)
			if err != nil {
				t.Fatalf("construct canary request: %v", err)
			}
			req.Header.Set("X-Canary", "1")
			if body, err := issueRequest(client, req); err == nil {
				canaryResp = body
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	if stableResp != "stable:/hello" {
		t.Fatalf("unexpected stable response: %q", stableResp)
	}
	if canaryResp != "canary:/hello" {
		t.Fatalf("unexpected canary response: %q", canaryResp)
	}

	stopCtx, cancelStop := stdcontext.WithTimeout(stdcontext.Background(), 5*time.Second)
	defer cancelStop()
	if err := deployment.Stop(stopCtx, events); err != nil {
		t.Fatalf("stop deployment: %v", err)
	}

	close(events)
	drain.Wait()
}

func issueRequest(client *http.Client, req *http.Request) (string, error) {
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}

func allocatePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate port: %v", err)
	}
	defer listener.Close()
	addr := listener.Addr().(*net.TCPAddr)
	return addr.Port
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

type blockingRuntime struct {
	readyCh     chan struct{}
	readyOnce   sync.Once
	stopStarted chan struct{}
	stopOnce    sync.Once
	stopRelease <-chan struct{}
}

func newBlockingRuntime(stopRelease <-chan struct{}) *blockingRuntime {
	return &blockingRuntime{
		readyCh:     make(chan struct{}),
		stopStarted: make(chan struct{}),
		stopRelease: stopRelease,
	}
}

func (b *blockingRuntime) Start(ctx stdcontext.Context, spec runtime.StartSpec) (runtime.Handle, error) {
	_ = spec
	go func() {
		time.Sleep(10 * time.Millisecond)
		b.readyOnce.Do(func() {
			close(b.readyCh)
		})
	}()
	return &blockingInstance{runtime: b}, nil
}

type blockingInstance struct {
	runtime *blockingRuntime
}

func (i *blockingInstance) WaitReady(ctx stdcontext.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-i.runtime.readyCh:
		return nil
	}
}

func (i *blockingInstance) Wait(ctx stdcontext.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-i.runtime.stopRelease:
		return nil
	}
}

func (i *blockingInstance) Health() <-chan probe.State {
	return nil
}

func (i *blockingInstance) Stop(ctx stdcontext.Context) error {
	i.runtime.stopOnce.Do(func() {
		close(i.runtime.stopStarted)
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-i.runtime.stopRelease:
		return nil
	}
}

func (i *blockingInstance) Kill(ctx stdcontext.Context) error {
	return i.Stop(ctx)
}

func (i *blockingInstance) Logs(ctx stdcontext.Context) (<-chan runtime.LogEntry, error) {
	return nil, nil
}

type mockRuntime struct {
	mu          sync.Mutex
	starts      []string
	readies     []string
	stops       []string
	readyCh     map[string][]chan struct{}
	startErr    map[string]error
	waitErr     map[string]error
	waitErrOnce map[string]error
	stopErr     map[string]error
	logs        map[string][]runtime.LogEntry
	autoReady   bool
}

func newMockRuntime() *mockRuntime {
	return &mockRuntime{
		readyCh:     make(map[string][]chan struct{}),
		startErr:    make(map[string]error),
		waitErr:     make(map[string]error),
		waitErrOnce: make(map[string]error),
		stopErr:     make(map[string]error),
		logs:        make(map[string][]runtime.LogEntry),
		autoReady:   true,
	}
}

func (m *mockRuntime) Start(ctx stdcontext.Context, spec runtime.StartSpec) (runtime.Handle, error) {
	name := spec.Name
	m.mu.Lock()
	if err := m.startErr[name]; err != nil {
		m.mu.Unlock()
		return nil, err
	}
	m.starts = append(m.starts, name)
	readyCh := make(chan struct{})
	m.readyCh[name] = append(m.readyCh[name], readyCh)
	waitErr := m.waitErr[name]
	stopErr := m.stopErr[name]
	logLines := append([]runtime.LogEntry(nil), m.logs[name]...)
	autoReady := m.autoReady
	m.mu.Unlock()

	logsCh := make(chan runtime.LogEntry, len(logLines))
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
	queue := m.readyCh[name]
	var ch chan struct{}
	if len(queue) > 0 {
		ch = queue[0]
		if len(queue) == 1 {
			delete(m.readyCh, name)
		} else {
			m.readyCh[name] = queue[1:]
		}
	}
	m.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

func (m *mockRuntime) recordReady(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readies = append(m.readies, name)
}

func (m *mockRuntime) consumeWaitOnce(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	err := m.waitErrOnce[name]
	if err != nil {
		delete(m.waitErrOnce, name)
	}
	return err
}

func (m *mockRuntime) FailNextReady(name string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		delete(m.waitErrOnce, name)
		return
	}
	m.waitErrOnce[name] = err
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
	logs     chan runtime.LogEntry
	waitErr  error
	stopErr  error
	stopOnce sync.Once
}

func (i *mockInstance) WaitReady(ctx stdcontext.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-i.ready:
		if onceErr := i.runtime.consumeWaitOnce(i.name); onceErr != nil {
			return onceErr
		}
		if i.waitErr != nil {
			return i.waitErr
		}
		i.runtime.recordReady(i.name)
		return nil
	}
}

func (i *mockInstance) Wait(ctx stdcontext.Context) error {
	if i.waitErr != nil {
		return i.waitErr
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (i *mockInstance) Health() <-chan probe.State {
	return nil
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

func (i *mockInstance) Kill(ctx stdcontext.Context) error {
	return i.Stop(ctx)
}

func (i *mockInstance) Logs(ctx stdcontext.Context) (<-chan runtime.LogEntry, error) {
	return i.logs, nil
}

type httpRuntime struct {
	mu        sync.Mutex
	instances map[string]*httpInstance
}

func newHTTPRuntime() *httpRuntime {
	return &httpRuntime{instances: make(map[string]*httpInstance)}
}

func (h *httpRuntime) Start(ctx stdcontext.Context, spec runtime.StartSpec) (runtime.Handle, error) {
	addr := spec.Env["LISTEN_ADDR"]
	if addr == "" {
		return nil, fmt.Errorf("service %s requires LISTEN_ADDR", spec.Name)
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}

	response := spec.Env["RESPONSE"]
	if response == "" {
		response = spec.Name
	}

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "%s:%s", response, r.URL.Path)
		}),
	}

	inst := &httpInstance{
		server:   srv,
		listener: listener,
		ready:    make(chan struct{}),
		done:     make(chan struct{}),
	}

	h.mu.Lock()
	h.instances[spec.Name] = inst
	h.mu.Unlock()

	go inst.serve()
	close(inst.ready)

	go func() {
		<-ctx.Done()
		_ = inst.server.Shutdown(stdcontext.Background())
	}()

	return inst, nil
}

type httpInstance struct {
	server   *http.Server
	listener net.Listener
	ready    chan struct{}
	done     chan struct{}

	stopOnce sync.Once
	waitErr  error
}

func (i *httpInstance) WaitReady(ctx stdcontext.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-i.ready:
		return nil
	}
}

func (i *httpInstance) Wait(ctx stdcontext.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-i.done:
		return i.waitErr
	}
}

func (i *httpInstance) Health() <-chan probe.State {
	return nil
}

func (i *httpInstance) Stop(ctx stdcontext.Context) error {
	var err error
	i.stopOnce.Do(func() {
		err = i.server.Shutdown(ctx)
	})
	return err
}

func (i *httpInstance) Kill(ctx stdcontext.Context) error {
	var err error
	i.stopOnce.Do(func() {
		err = i.server.Close()
	})
	return err
}

func (i *httpInstance) Logs(ctx stdcontext.Context) (<-chan runtime.LogEntry, error) {
	return nil, nil
}

func (i *httpInstance) serve() {
	err := i.server.Serve(i.listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		i.waitErr = err
	}
	close(i.done)
}
