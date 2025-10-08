package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/Paintersrp/orco/internal/logmux"
)

type sinkConfigView struct {
	Directory    string
	MaxFileSize  int64
	MaxTotalSize int64
	MaxFileAge   time.Duration
	MaxFileCount int
}

func extractSinkConfig(t *testing.T, opts []logmux.SinkOption) sinkConfigView {
	t.Helper()
	mux := logmux.New(1, opts...)
	t.Cleanup(func() {
		mux.Close()
	})
	muxVal := reflect.ValueOf(mux).Elem()
	sinkField := muxVal.FieldByName("sink")
	if sinkField.IsNil() {
		return sinkConfigView{}
	}
	sinkPtr := unsafe.Pointer(sinkField.Pointer())
	sinkVal := reflect.NewAt(sinkField.Type().Elem(), sinkPtr).Elem()
	cfgField := sinkVal.FieldByName("cfg")
	cfgPtr := unsafe.Pointer(cfgField.UnsafeAddr())
	cfgVal := reflect.NewAt(cfgField.Type(), cfgPtr).Elem()
	return sinkConfigView{
		Directory:    cfgVal.FieldByName("directory").String(),
		MaxFileSize:  cfgVal.FieldByName("maxFileSize").Int(),
		MaxTotalSize: cfgVal.FieldByName("maxTotalSize").Int(),
		MaxFileAge:   time.Duration(cfgVal.FieldByName("maxFileAge").Int()),
		MaxFileCount: int(cfgVal.FieldByName("maxFileCount").Int()),
	}
}

func TestRootCommandLogRetentionFromEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ORCO_LOG_DIR", dir)
	t.Setenv("ORCO_LOG_MAX_FILE_SIZE", "128")
	t.Setenv("ORCO_LOG_MAX_TOTAL_SIZE", "256")
	t.Setenv("ORCO_LOG_MAX_FILE_AGE", "30s")
	t.Setenv("ORCO_LOG_MAX_FILE_COUNT", "3")

	cmd, ctx := newRootCommand()
	if ctx.logRetention.Directory != dir {
		t.Fatalf("expected directory %s, got %s", dir, ctx.logRetention.Directory)
	}

	cfg := extractSinkConfig(t, ctx.logSinkOptions())
	if cfg.Directory != dir {
		t.Fatalf("expected sink directory %s, got %s", dir, cfg.Directory)
	}
	if cfg.MaxFileSize != 128 {
		t.Fatalf("expected max file size 128, got %d", cfg.MaxFileSize)
	}
	if cfg.MaxTotalSize != 256 {
		t.Fatalf("expected max total size 256, got %d", cfg.MaxTotalSize)
	}
	if cfg.MaxFileAge != 30*time.Second {
		t.Fatalf("expected max file age 30s, got %s", cfg.MaxFileAge)
	}
	if cfg.MaxFileCount != 3 {
		t.Fatalf("expected max file count 3, got %d", cfg.MaxFileCount)
	}

	// Avoid unused variable warning for the command.
	_ = cmd
}

func TestRootCommandLogRetentionFlagsOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ORCO_LOG_DIR", dir)
	t.Setenv("ORCO_LOG_MAX_FILE_SIZE", "64")
	t.Setenv("ORCO_LOG_MAX_TOTAL_SIZE", "128")
	t.Setenv("ORCO_LOG_MAX_FILE_AGE", "45s")
	t.Setenv("ORCO_LOG_MAX_FILE_COUNT", "2")

	cmd, ctx := newRootCommand()
	overrideDir := t.TempDir()
	if err := cmd.PersistentFlags().Set("log-dir", overrideDir); err != nil {
		t.Fatalf("set log-dir flag: %v", err)
	}
	if err := cmd.PersistentFlags().Set("log-max-file-size", "256"); err != nil {
		t.Fatalf("set log-max-file-size flag: %v", err)
	}
	if err := cmd.PersistentFlags().Set("log-max-total-size", "512"); err != nil {
		t.Fatalf("set log-max-total-size flag: %v", err)
	}
	if err := cmd.PersistentFlags().Set("log-max-file-age", "1m30s"); err != nil {
		t.Fatalf("set log-max-file-age flag: %v", err)
	}
	if err := cmd.PersistentFlags().Set("log-max-files", "5"); err != nil {
		t.Fatalf("set log-max-files flag: %v", err)
	}

	if ctx.logRetention.Directory != overrideDir {
		t.Fatalf("expected overridden directory %s, got %s", overrideDir, ctx.logRetention.Directory)
	}

	cfg := extractSinkConfig(t, ctx.logSinkOptions())
	if cfg.Directory != overrideDir {
		t.Fatalf("expected sink directory %s, got %s", overrideDir, cfg.Directory)
	}
	if cfg.MaxFileSize != 256 {
		t.Fatalf("expected max file size 256, got %d", cfg.MaxFileSize)
	}
	if cfg.MaxTotalSize != 512 {
		t.Fatalf("expected max total size 512, got %d", cfg.MaxTotalSize)
	}
	if cfg.MaxFileAge != 90*time.Second {
		t.Fatalf("expected max file age 90s, got %s", cfg.MaxFileAge)
	}
	if cfg.MaxFileCount != 5 {
		t.Fatalf("expected max file count 5, got %d", cfg.MaxFileCount)
	}

	_ = cmd
}

func TestContextLoadStackAppliesLoggingConfig(t *testing.T) {
	cmd, ctx := newRootCommand()
	_ = cmd

	stackDir := t.TempDir()
	stackPath := filepath.Join(stackDir, "stack.yaml")
	contents := `version: "0.1"

stack:
  name: demo
  workdir: .

logging:
  directory: ./logs
  maxFileSize: 128
  maxTotalSize: 256
  maxFileAge: 45s
  maxFileCount: 4

services:
  api:
    runtime: process
    command: ["/bin/true"]
    health:
      http:
        url: http://localhost:8080/healthz
        expectStatus: [200]
`
	if err := os.WriteFile(stackPath, []byte(contents), 0o644); err != nil {
		t.Fatalf("write stack file: %v", err)
	}

	*ctx.stackFile = stackPath
	doc, err := ctx.loadStack()
	if err != nil {
		t.Fatalf("load stack: %v", err)
	}
	t.Cleanup(func() {
		if doc != nil && doc.Graph != nil {
			_ = doc.Graph
		}
	})

	expectedDir := filepath.Join(doc.File.Stack.Workdir, "logs")
	if ctx.logRetention.Directory != expectedDir {
		t.Fatalf("expected log directory %s, got %s", expectedDir, ctx.logRetention.Directory)
	}
	if ctx.logRetention.MaxFileSize != 128 {
		t.Fatalf("expected max file size 128, got %d", ctx.logRetention.MaxFileSize)
	}
	if ctx.logRetention.MaxTotalSize != 256 {
		t.Fatalf("expected max total size 256, got %d", ctx.logRetention.MaxTotalSize)
	}
	if ctx.logRetention.MaxFileAge != 45*time.Second {
		t.Fatalf("expected max file age 45s, got %s", ctx.logRetention.MaxFileAge)
	}
	if ctx.logRetention.MaxFileCount != 4 {
		t.Fatalf("expected max file count 4, got %d", ctx.logRetention.MaxFileCount)
	}
}

func TestContextLoadStackLoggingRespectsOverrides(t *testing.T) {
	cmd, ctx := newRootCommand()

	overrideDir := t.TempDir()
	if err := cmd.PersistentFlags().Set("log-dir", overrideDir); err != nil {
		t.Fatalf("set log-dir flag: %v", err)
	}
	if err := cmd.PersistentFlags().Set("log-max-file-size", "512"); err != nil {
		t.Fatalf("set log-max-file-size flag: %v", err)
	}
	if err := cmd.PersistentFlags().Set("log-max-total-size", "1024"); err != nil {
		t.Fatalf("set log-max-total-size flag: %v", err)
	}
	if err := cmd.PersistentFlags().Set("log-max-file-age", "90s"); err != nil {
		t.Fatalf("set log-max-file-age flag: %v", err)
	}
	if err := cmd.PersistentFlags().Set("log-max-files", "6"); err != nil {
		t.Fatalf("set log-max-files flag: %v", err)
	}

	stackDir := t.TempDir()
	stackPath := filepath.Join(stackDir, "stack.yaml")
	if err := os.WriteFile(stackPath, []byte(`version: "0.1"

stack:
  name: demo
  workdir: .

logging:
  directory: ./logs
  maxFileSize: 128
  maxTotalSize: 256
  maxFileAge: 30s
  maxFileCount: 3

services:
  api:
    runtime: process
    command: ["/bin/true"]
    health:
      http:
        url: http://localhost:8080/healthz
        expectStatus: [200]
`), 0o644); err != nil {
		t.Fatalf("write stack file: %v", err)
	}

	*ctx.stackFile = stackPath
	if _, err := ctx.loadStack(); err != nil {
		t.Fatalf("load stack: %v", err)
	}

	if ctx.logRetention.Directory != overrideDir {
		t.Fatalf("expected directory override %s, got %s", overrideDir, ctx.logRetention.Directory)
	}
	if ctx.logRetention.MaxFileSize != 512 {
		t.Fatalf("expected max file size override 512, got %d", ctx.logRetention.MaxFileSize)
	}
	if ctx.logRetention.MaxTotalSize != 1024 {
		t.Fatalf("expected max total size override 1024, got %d", ctx.logRetention.MaxTotalSize)
	}
	if ctx.logRetention.MaxFileAge != 90*time.Second {
		t.Fatalf("expected max file age override 90s, got %s", ctx.logRetention.MaxFileAge)
	}
	if ctx.logRetention.MaxFileCount != 6 {
		t.Fatalf("expected max file count override 6, got %d", ctx.logRetention.MaxFileCount)
	}

	_ = cmd
}
