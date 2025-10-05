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
	if !strings.Contains(err.Error(), "stack.name") {
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

func TestLoadProcessRuntimeRequiresCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	manifest := []byte(`version: 0.1
stack:
  name: demo
services:
  worker:
    runtime: process
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
	if !strings.Contains(err.Error(), "multiple probe types") {
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
	if !strings.Contains(err.Error(), "services.api.health") {
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
