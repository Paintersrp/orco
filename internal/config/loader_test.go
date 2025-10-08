package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadValidStack(t *testing.T) {
	dir := t.TempDir()
	workdir := filepath.Join(dir, "app")
	if err := os.Mkdir(workdir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	envFile := filepath.Join(workdir, "vars.env")
	if err := os.WriteFile(envFile, []byte("TOKEN=${FILE_SECRET}\nPASSWORD=from-file"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	t.Setenv("FILE_SECRET", "alpha")
	t.Setenv("WORKDIR_PATH", "./app")
	t.Setenv("ENV_FILE", "./vars.env")
	t.Setenv("API_PASSWORD", "s3cr3t")

	stackPath := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
  workdir: ${WORKDIR_PATH}
services:
  api:
    image: ghcr.io/demo/api:latest
    runtime: docker
    env:
      PASSWORD: ${API_PASSWORD}
    envFromFile: ${ENV_FILE}
    ports: ["8080:8080"]
    health:
      http:
        url: http://localhost:8080/health
      log:
        pattern: "ready"
        sources: [stderr]
      expression: http || log
`)
	if err := os.WriteFile(stackPath, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	doc, err := Load(stackPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got, want := doc.Stack.Workdir, workdir; got != want {
		t.Fatalf("unexpected workdir: got %q want %q", got, want)
	}
	svc := doc.Services["api"]
	if svc == nil {
		t.Fatalf("service api missing")
	}
	if got, want := svc.ResolvedWorkdir, workdir; got != want {
		t.Fatalf("resolved workdir mismatch: got %q want %q", got, want)
	}
	if got, want := svc.Env["TOKEN"], "alpha"; got != want {
		t.Fatalf("env file value mismatch: got %q want %q", got, want)
	}
	if got, want := svc.Env["PASSWORD"], "s3cr3t"; got != want {
		t.Fatalf("env expansion mismatch: got %q want %q", got, want)
	}
	if got, want := svc.EnvFromFile, envFile; got != want {
		t.Fatalf("envFromFile not resolved: got %q want %q", got, want)
	}
	if got, want := svc.Replicas, 1; got != want {
		t.Fatalf("replicas default mismatch: got %d want %d", got, want)
	}
	if svc.Health == nil {
		t.Fatalf("health probe not loaded")
	}
	if svc.Health.Log == nil {
		t.Fatalf("log probe not loaded")
	}
	if got, want := svc.Health.Log.Pattern, "ready"; got != want {
		t.Fatalf("log pattern mismatch: got %q want %q", got, want)
	}
	if got, want := svc.Health.Log.Sources, []string{"stderr"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("log sources mismatch: got %#v want %#v", got, want)
	}
	if got, want := svc.Health.Expression, "http || log"; got != want {
		t.Fatalf("expression mismatch: got %q want %q", got, want)
	}
	if got, want := svc.Health.Interval.Duration, 2*time.Second; got != want {
		t.Fatalf("interval default mismatch: got %v want %v", got, want)
	}
	if got, want := svc.Health.Timeout.Duration, time.Second; got != want {
		t.Fatalf("timeout default mismatch: got %v want %v", got, want)
	}
	if got, want := svc.Health.FailureThreshold, 3; got != want {
		t.Fatalf("failure threshold mismatch: got %d want %d", got, want)
	}
	if got, want := svc.Health.SuccessThreshold, 1; got != want {
		t.Fatalf("success threshold mismatch: got %d want %d", got, want)
	}
}

func TestLoadEnvDefaultFallback(t *testing.T) {
	dir := t.TempDir()
	workdir := filepath.Join(dir, "app")
	if err := os.Mkdir(workdir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	envFile := filepath.Join(workdir, "vars.env")
	envFileContents := strings.Join([]string{
		"FILE_ABSENT=${FILE_ABSENT:-file-default}",
		"FILE_EMPTY=${FILE_EMPTY:-file-empty}",
		"",
	}, "\n")
	if err := os.WriteFile(envFile, []byte(envFileContents), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	t.Setenv("STACK_WORKDIR", "")
	t.Setenv("INLINE_EMPTY", "")
	t.Setenv("ENV_FILE", "")
	t.Setenv("FILE_EMPTY", "")
	t.Setenv("DATA_DIR", "")
	t.Setenv("LOG_DIR", "")

	stackPath := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: defaults
  workdir: ${STACK_WORKDIR:-./app}
logging:
  directory: ${LOG_DIR:-logs}
services:
  api:
    image: ghcr.io/demo/api:latest
    runtime: docker
    env:
      INLINE_ABSENT: ${INLINE_ABSENT:-inline-default}
      INLINE_EMPTY: ${INLINE_EMPTY:-inline-empty}
    envFromFile: ${ENV_FILE:-./vars.env}
    volumes: ["${DATA_DIR:-./data}:/var/data"]
    health:
      tcp:
        address: localhost:1234
`)
	if err := os.WriteFile(stackPath, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	doc, err := Load(stackPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got, want := doc.Stack.Workdir, workdir; got != want {
		t.Fatalf("workdir fallback mismatch: got %q want %q", got, want)
	}
	if doc.Logging == nil {
		t.Fatalf("logging configuration missing")
	}
	if got, want := doc.Logging.Directory, filepath.Join(workdir, "logs"); got != want {
		t.Fatalf("logging directory fallback mismatch: got %q want %q", got, want)
	}

	svc := doc.Services["api"]
	if svc == nil {
		t.Fatalf("service api missing")
	}
	if got, want := svc.EnvFromFile, envFile; got != want {
		t.Fatalf("envFromFile fallback mismatch: got %q want %q", got, want)
	}
	if got, want := svc.Env["INLINE_ABSENT"], "inline-default"; got != want {
		t.Fatalf("inline absent env mismatch: got %q want %q", got, want)
	}
	if got, want := svc.Env["INLINE_EMPTY"], "inline-empty"; got != want {
		t.Fatalf("inline empty env mismatch: got %q want %q", got, want)
	}
	if got, want := svc.Env["FILE_ABSENT"], "file-default"; got != want {
		t.Fatalf("file absent env mismatch: got %q want %q", got, want)
	}
	if got, want := svc.Env["FILE_EMPTY"], "file-empty"; got != want {
		t.Fatalf("file empty env mismatch: got %q want %q", got, want)
	}
	expectedVolume := filepath.Join(workdir, "data") + ":/var/data"
	if len(svc.Volumes) != 1 || svc.Volumes[0] != expectedVolume {
		t.Fatalf("volume fallback mismatch: got %#v want %q", svc.Volumes, expectedVolume)
	}
}

func TestLoadMissingStackName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack: {}
services:
  api:
    image: test
    runtime: docker
    health:
      tcp:
        address: localhost:1234
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "stack: missing properties") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error does not contain stack path: %v", err)
	}
}

func TestLoadDockerRuntimeRequiresImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
services:
  api:
    runtime: docker
    health:
      tcp:
        address: localhost:1234
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "services.api.image") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSchemaValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
services: []
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "schema validation failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "services") {
		t.Fatalf("schema error does not mention services path: %v", err)
	}
}

func TestLoadPodmanRuntimeRequiresImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
services:
  api:
    runtime: podman
    health:
      tcp:
        address: localhost:1234
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "services.api.image") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadValidPodmanStack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
services:
  api:
    image: ghcr.io/demo/api:latest
    runtime: podman
    health:
      tcp:
        address: localhost:1234
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	doc, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	svc := doc.Services["api"]
	if svc == nil {
		t.Fatalf("service api missing")
	}
	if got, want := svc.Runtime, "podman"; got != want {
		t.Fatalf("runtime mismatch: got %q want %q", got, want)
	}
	if got, want := svc.Image, "ghcr.io/demo/api:latest"; got != want {
		t.Fatalf("image mismatch: got %q want %q", got, want)
	}
}

func TestLoadProcessRuntimeRequiresCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
services:
  worker:
    runtime: process
    health:
      tcp:
        address: localhost:1234
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "services.worker.command") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadServiceOverridesTimingInheritsDefaultProbe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
defaults:
  health:
    interval: 2s
    timeout: 1s
    http:
      url: http://localhost:8080/health
      expectStatus: [200, 204]
services:
  api:
    image: ghcr.io/demo/api:latest
    runtime: docker
    health:
      timeout: 5s
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	doc, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	svc := doc.Services["api"]
	if svc == nil {
		t.Fatalf("service api missing")
	}
	if svc.Health == nil {
		t.Fatalf("health probe not loaded")
	}
	if svc.Health.HTTP == nil {
		t.Fatalf("http probe not inherited from defaults")
	}
	if got, want := svc.Health.HTTP.URL, "http://localhost:8080/health"; got != want {
		t.Fatalf("http url mismatch: got %q want %q", got, want)
	}
	if got, want := svc.Health.HTTP.ExpectStatus, []int{200, 204}; !equalIntSlices(got, want) {
		t.Fatalf("http expectStatus mismatch: got %v want %v", got, want)
	}
	if got, want := svc.Health.Timeout.Duration, 5*time.Second; got != want {
		t.Fatalf("timeout override mismatch: got %v want %v", got, want)
	}
}

func TestLoadProxyConfiguration(t *testing.T) {
	dir := t.TempDir()
	workdir := filepath.Join(dir, "app")
	staticDir := filepath.Join(workdir, "static")
	if err := os.MkdirAll(staticDir, 0o755); err != nil {
		t.Fatalf("mkdir static dir: %v", err)
	}

	t.Setenv("WORKDIR_PATH", "./app")
	t.Setenv("STATIC_DIR", "./static")
	t.Setenv("ROUTE_REGION", "us-west-2")

	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
  workdir: ${WORKDIR_PATH}
proxy:
  assets:
    directory: ${STATIC_DIR}
    index: index.html
  routes:
    - pathPrefix: /api/
      headers:
        X-Region: ${ROUTE_REGION}
      service: api
      port: 8080
      stripPathPrefix: true
services:
  api:
    image: ghcr.io/demo/api:latest
    runtime: docker
    health:
      tcp:
        address: localhost:8080
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	stack, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if stack.Proxy == nil {
		t.Fatalf("proxy configuration missing")
	}
	if got, want := len(stack.Proxy.Routes), 1; got != want {
		t.Fatalf("unexpected route count: got %d want %d", got, want)
	}
	route := stack.Proxy.Routes[0]
	if route == nil {
		t.Fatalf("route was nil")
	}
	if got, want := route.PathPrefix, "/api/"; got != want {
		t.Fatalf("path prefix mismatch: got %q want %q", got, want)
	}
	if got, want := route.Headers["X-Region"], "us-west-2"; got != want {
		t.Fatalf("header expansion mismatch: got %q want %q", got, want)
	}
	if got, want := stack.Proxy.Assets.Directory, staticDir; got != want {
		t.Fatalf("asset directory not resolved: got %q want %q", got, want)
	}
	if got, want := stack.Proxy.Assets.Index, filepath.Join(staticDir, "index.html"); got != want {
		t.Fatalf("asset index not resolved: got %q want %q", got, want)
	}
}

func TestLoadProxyInvalidRoute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
proxy:
  routes:
    - pathPrefix: /api
      service: missing
      port: 8080
services:
  api:
    image: ghcr.io/demo/api:latest
    runtime: docker
    health:
      tcp:
        address: localhost:8080
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "proxy.routes[0].service") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error does not mention missing service: %v", err)
	}
}

func TestLoadIncludesMerge(t *testing.T) {
	dir := t.TempDir()

	basePath := filepath.Join(dir, "base.yaml")
	baseManifest := []byte(`version: 0.1
stack:
  name: base
  workdir: app
services:
  api:
    image: ghcr.io/demo/api:base
    runtime: docker
    health:
      tcp:
        address: localhost:8080
`)
	if err := os.WriteFile(basePath, baseManifest, 0o644); err != nil {
		t.Fatalf("write base include: %v", err)
	}

	extraPath := filepath.Join(dir, "extra.yaml")
	extraManifest := []byte(`services:
  worker:
    image: ghcr.io/demo/worker:latest
    runtime: docker
    health:
      tcp:
        address: localhost:9090
`)
	if err := os.WriteFile(extraPath, extraManifest, 0o644); err != nil {
		t.Fatalf("write extra include: %v", err)
	}

	stackPath := filepath.Join(dir, "stack.yaml")
	stackManifest := []byte(`includes:
  - base.yaml
  - extra.yaml
stack:
  name: final
services:
  api:
    image: ghcr.io/demo/api:final
`)
	if err := os.WriteFile(stackPath, stackManifest, 0o644); err != nil {
		t.Fatalf("write stack manifest: %v", err)
	}

	stack, err := Load(stackPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got, want := stack.Version, "0.1"; got != want {
		t.Fatalf("version mismatch: got %q want %q", got, want)
	}
	if got, want := stack.Stack.Name, "final"; got != want {
		t.Fatalf("stack name mismatch: got %q want %q", got, want)
	}
	if got, want := stack.Stack.Workdir, filepath.Join(dir, "app"); got != want {
		t.Fatalf("workdir mismatch: got %q want %q", got, want)
	}
	if got, want := len(stack.Includes), 2; got != want {
		t.Fatalf("unexpected include count: got %d want %d", got, want)
	}
	if stack.Includes[0] != "base.yaml" || stack.Includes[1] != "extra.yaml" {
		t.Fatalf("includes not preserved: got %v", stack.Includes)
	}

	api := stack.Services["api"]
	if api == nil {
		t.Fatalf("api service missing")
	}
	if got, want := api.Image, "ghcr.io/demo/api:final"; got != want {
		t.Fatalf("api image mismatch: got %q want %q", got, want)
	}
	if got, want := api.Runtime, "docker"; got != want {
		t.Fatalf("api runtime mismatch: got %q want %q", got, want)
	}
	if api.Health == nil {
		t.Fatalf("api health not merged")
	}

	worker := stack.Services["worker"]
	if worker == nil {
		t.Fatalf("worker service missing")
	}
	if got, want := worker.Image, "ghcr.io/demo/worker:latest"; got != want {
		t.Fatalf("worker image mismatch: got %q want %q", got, want)
	}
	if worker.Health == nil {
		t.Fatalf("worker health not merged")
	}
}

func TestLoadIncludeOverridePrecedence(t *testing.T) {
	dir := t.TempDir()

	basePath := filepath.Join(dir, "base.yaml")
	baseManifest := []byte(`version: 0.1
stack:
  name: demo
services:
  api:
    image: ghcr.io/demo/api:base
    runtime: docker
    health:
      tcp:
        address: localhost:8080
`)
	if err := os.WriteFile(basePath, baseManifest, 0o644); err != nil {
		t.Fatalf("write base include: %v", err)
	}

	overridePath := filepath.Join(dir, "override.yaml")
	overrideManifest := []byte(`services:
  api:
    image: ghcr.io/demo/api:override
`)
	if err := os.WriteFile(overridePath, overrideManifest, 0o644); err != nil {
		t.Fatalf("write override include: %v", err)
	}

	stackPath := filepath.Join(dir, "stack.yaml")
	stackManifest := []byte(`includes:
  - base.yaml
  - override.yaml
`)
	if err := os.WriteFile(stackPath, stackManifest, 0o644); err != nil {
		t.Fatalf("write stack manifest: %v", err)
	}

	stack, err := Load(stackPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	api := stack.Services["api"]
	if api == nil {
		t.Fatalf("api service missing")
	}
	if got, want := api.Image, "ghcr.io/demo/api:override"; got != want {
		t.Fatalf("override precedence failed: got %q want %q", got, want)
	}
	if api.Health == nil {
		t.Fatalf("api health missing after merge")
	}
}

func TestLoadIncludeMissingFile(t *testing.T) {
	dir := t.TempDir()

	stackPath := filepath.Join(dir, "stack.yaml")
	stackManifest := []byte(`version: 0.1
includes:
  - missing.yaml
stack:
  name: demo
services:
  api:
    image: ghcr.io/demo/api:latest
    runtime: docker
    health:
      tcp:
        address: localhost:8080
`)
	if err := os.WriteFile(stackPath, stackManifest, 0o644); err != nil {
		t.Fatalf("write stack manifest: %v", err)
	}

	_, err := Load(stackPath)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "missing.yaml") {
		t.Fatalf("error does not mention missing include: %v", err)
	}
	if !strings.Contains(err.Error(), "include") {
		t.Fatalf("error does not mention include context: %v", err)
	}
}

func TestLoadIncludeCycle(t *testing.T) {
	dir := t.TempDir()

	aPath := filepath.Join(dir, "a.yaml")
	bPath := filepath.Join(dir, "b.yaml")

	aManifest := []byte(`includes:
  - b.yaml
version: 0.1
stack:
  name: demo
services:
  api:
    image: ghcr.io/demo/api:a
    runtime: docker
    health:
      tcp:
        address: localhost:8080
`)
	if err := os.WriteFile(aPath, aManifest, 0o644); err != nil {
		t.Fatalf("write a manifest: %v", err)
	}

	bManifest := []byte(`includes:
  - a.yaml
services:
  api:
    image: ghcr.io/demo/api:b
`)
	if err := os.WriteFile(bPath, bManifest, 0o644); err != nil {
		t.Fatalf("write b manifest: %v", err)
	}

	_, err := Load(aPath)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error does not report cycle: %v", err)
	}
	if !strings.Contains(err.Error(), filepath.Base(aPath)) || !strings.Contains(err.Error(), filepath.Base(bPath)) {
		t.Fatalf("error does not include cycle members: %v", err)
	}
}

func TestLoadIncludeEnvironmentExpansion(t *testing.T) {
	dir := t.TempDir()

	basePath := filepath.Join(dir, "base.yaml")
	baseManifest := []byte(`version: 0.1
stack:
  name: ${STACK_NAME:-base}
  workdir: ${WORKDIR:-./app}
services:
  api:
    image: ${API_IMAGE:-ghcr.io/demo/api:base}
    runtime: docker
    command: ["run", "${API_COMMAND:-serve}"]
    health:
      tcp:
        address: localhost:8080
`)
	if err := os.WriteFile(basePath, baseManifest, 0o644); err != nil {
		t.Fatalf("write base manifest: %v", err)
	}

	stackPath := filepath.Join(dir, "stack.yaml")
	stackManifest := []byte(`includes:
  - ${STACK_INCLUDE:-base.yaml}
version: ${STACK_VERSION:-0.2}
services:
  api:
    image: ${ROOT_IMAGE:-ghcr.io/demo/api:root}
`)
	if err := os.WriteFile(stackPath, stackManifest, 0o644); err != nil {
		t.Fatalf("write stack manifest: %v", err)
	}

	t.Setenv("STACK_NAME", "demo-from-env")
	t.Setenv("STACK_VERSION", "0.3")
	t.Setenv("API_IMAGE", "ghcr.io/demo/api:env")
	t.Setenv("ROOT_IMAGE", "ghcr.io/demo/api:root-env")

	stack, err := Load(stackPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got, want := stack.Version, "0.3"; got != want {
		t.Fatalf("version mismatch: got %q want %q", got, want)
	}
	if got, want := stack.Stack.Name, "demo-from-env"; got != want {
		t.Fatalf("stack name mismatch: got %q want %q", got, want)
	}
	expectedWorkdir := filepath.Join(dir, "app")
	if got, want := stack.Stack.Workdir, expectedWorkdir; got != want {
		t.Fatalf("workdir mismatch: got %q want %q", got, want)
	}
	if got, want := len(stack.Includes), 1; got != want {
		t.Fatalf("unexpected include count: got %d want %d", got, want)
	}
	if stack.Includes[0] != "base.yaml" {
		t.Fatalf("include reference not expanded: got %q want %q", stack.Includes[0], "base.yaml")
	}

	svc := stack.Services["api"]
	if svc == nil {
		t.Fatalf("api service missing")
	}
	if got, want := svc.Image, "ghcr.io/demo/api:root-env"; got != want {
		t.Fatalf("service image mismatch: got %q want %q", got, want)
	}
	if len(svc.Command) != 2 || svc.Command[0] != "run" || svc.Command[1] != "serve" {
		t.Fatalf("command not expanded: got %#v", svc.Command)
	}
	if svc.Health == nil || svc.Health.TCP == nil {
		t.Fatalf("tcp health missing after merge")
	}
	if got, want := svc.Health.TCP.Address, "localhost:8080"; got != want {
		t.Fatalf("tcp address mismatch: got %q want %q", got, want)
	}
}

func TestLoadServiceGracePeriodZeroOverridesDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
defaults:
  health:
    gracePeriod: 45s
    interval: 2s
    timeout: 1s
    http:
      url: http://localhost:8080/health
services:
  api:
    image: ghcr.io/demo/api:latest
    runtime: docker
    health:
      gracePeriod: 0s
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	doc, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	svc := doc.Services["api"]
	if svc == nil {
		t.Fatalf("service api missing")
	}
	if svc.Health == nil {
		t.Fatalf("health probe not loaded")
	}
	if got, want := svc.Health.GracePeriod.Duration, time.Duration(0); got != want {
		t.Fatalf("grace period override mismatch: got %v want %v", got, want)
	}
	if got, want := svc.Health.Interval.Duration, 2*time.Second; got != want {
		t.Fatalf("interval default mismatch: got %v want %v", got, want)
	}
	if got, want := svc.Health.Timeout.Duration, time.Second; got != want {
		t.Fatalf("timeout default mismatch: got %v want %v", got, want)
	}
}

func TestLoadServiceOverridesProbeType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
defaults:
  health:
    interval: 3s
    http:
      url: http://localhost:8080/health
services:
  api:
    image: ghcr.io/demo/api:latest
    runtime: docker
    health:
      tcp:
        address: localhost:5432
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	doc, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	svc := doc.Services["api"]
	if svc == nil {
		t.Fatalf("service api missing")
	}
	if svc.Health == nil {
		t.Fatalf("health probe not loaded")
	}
	if svc.Health.HTTP != nil {
		t.Fatalf("unexpected http probe inherited from defaults")
	}
	if svc.Health.TCP == nil {
		t.Fatalf("tcp probe not applied from service override")
	}
	if got, want := svc.Health.TCP.Address, "localhost:5432"; got != want {
		t.Fatalf("tcp address mismatch: got %q want %q", got, want)
	}
	if got, want := svc.Health.Interval.Duration, 3*time.Second; got != want {
		t.Fatalf("interval default mismatch: got %v want %v", got, want)
	}
}

func TestLoadMultipleProbes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
services:
  api:
    image: test
    runtime: docker
    ports: ["8080:8080"]
    health:
      http:
        url: http://localhost:8080/health
      tcp:
        address: localhost:8080
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "expression") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadServiceMissingHealthFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
services:
  api:
    image: ghcr.io/demo/api:latest
    runtime: docker
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "services.api: missing properties") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadResolvesVolumeSpecs(t *testing.T) {
	dir := t.TempDir()
	workdir := filepath.Join(dir, "app")
	if err := os.Mkdir(workdir, 0o755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	t.Setenv("CACHE_PATH", "./cache")

	stackPath := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
  workdir: ./app
services:
  api:
    image: ghcr.io/demo/api:latest
    runtime: docker
    volumes:
      - ./data:/var/lib/data
      - ${CACHE_PATH}:/cache:ro
    health:
      tcp:
        address: localhost:1234
`)
	if err := os.WriteFile(stackPath, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	doc, err := Load(stackPath)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	svc := doc.Services["api"]
	if svc == nil {
		t.Fatalf("service api missing")
	}

	want := []string{
		filepath.Join(workdir, "data") + ":/var/lib/data",
		filepath.Join(workdir, "cache") + ":/cache:ro",
	}
	if got := svc.Volumes; len(got) != len(want) {
		t.Fatalf("unexpected volumes length: got %d want %d", len(got), len(want))
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("volume %d mismatch: got %q want %q", i, got[i], want[i])
			}
		}
	}
}

func TestLoadRejectsInvalidVolumeSpec(t *testing.T) {
	dir := t.TempDir()
	stackPath := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
services:
  api:
    image: ghcr.io/demo/api:latest
    runtime: docker
    volumes: [":/app"]
    health:
      tcp:
        address: localhost:1234
`)
	if err := os.WriteFile(stackPath, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	_, err := Load(stackPath)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "services.api.volumes[0]") {
		t.Fatalf("error missing field path: %v", err)
	}
	if !strings.Contains(err.Error(), "host path is required") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadMalformedPort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
services:
  api:
    image: test
    runtime: docker
    ports: ["bad"]
    health:
      tcp:
        address: localhost:8080
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Invalid containerPort") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadNonNumericPort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
services:
  api:
    image: test
    runtime: docker
    ports: ["abc:8080"]
    health:
      tcp:
        address: localhost:8080
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Invalid hostPort") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadOutOfRangePort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
services:
  api:
    image: test
    runtime: docker
    ports: ["70000:8080"]
    health:
      tcp:
        address: localhost:8080
`)
	if err := os.WriteFile(path, manifest, 0o644); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Invalid hostPort") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadEnvFileSingleQuotedValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vars.env")
	contents := strings.Join([]string{
		"SINGLE='value with spaces'",
		"HASHED='value # with hash'",
		"COMMENT='value' # inline comment should be ignored",
		"# comment line should be ignored",
	}, "\n")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	values, err := loadEnvFile(path)
	if err != nil {
		t.Fatalf("loadEnvFile returned error: %v", err)
	}

	if got, want := values["SINGLE"], "value with spaces"; got != want {
		t.Fatalf("single-quoted value mismatch: got %q want %q", got, want)
	}
	if got, want := values["HASHED"], "value # with hash"; got != want {
		t.Fatalf("single-quoted hash value mismatch: got %q want %q", got, want)
	}
	if got, want := values["COMMENT"], "value"; got != want {
		t.Fatalf("single-quoted comment value mismatch: got %q want %q", got, want)
	}
}

func TestLoadEnvFileQuotedValuesWithInlineComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vars.env")
	contents := strings.Join([]string{
		"DOUBLE=\"value\" # inline comment",
		"DOUBLE_ESCAPED=\"value with \\\"quote\\\"\" # another comment",
		"SINGLE='value' # trailing comment",
		"SINGLE_HASH='value # still part of value' # end comment",
	}, "\n")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	values, err := loadEnvFile(path)
	if err != nil {
		t.Fatalf("loadEnvFile returned error: %v", err)
	}

	if got, want := values["DOUBLE"], "value"; got != want {
		t.Fatalf("double-quoted inline comment mismatch: got %q want %q", got, want)
	}
	if got, want := values["DOUBLE_ESCAPED"], "value with \"quote\""; got != want {
		t.Fatalf("double-quoted escaped value mismatch: got %q want %q", got, want)
	}
	if got, want := values["SINGLE"], "value"; got != want {
		t.Fatalf("single-quoted inline comment mismatch: got %q want %q", got, want)
	}
	if got, want := values["SINGLE_HASH"], "value # still part of value"; got != want {
		t.Fatalf("single-quoted hash inline comment mismatch: got %q want %q", got, want)
	}
}

func equalIntSlices(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
