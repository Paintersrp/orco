package cli

import (
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/Paintersrp/orco/internal/logmux"
)

type sinkConfigView struct {
	Directory    string
	Stack        string
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
		Stack:        cfgVal.FieldByName("stack").String(),
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

	cfg := extractSinkConfig(t, ctx.logSinkOptions("Demo"))
	if cfg.Directory != dir {
		t.Fatalf("expected sink directory %s, got %s", dir, cfg.Directory)
	}
	if cfg.Stack != "demo" {
		t.Fatalf("expected stack directory demo, got %s", cfg.Stack)
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

	cfg := extractSinkConfig(t, ctx.logSinkOptions("demo"))
	if cfg.Directory != overrideDir {
		t.Fatalf("expected sink directory %s, got %s", overrideDir, cfg.Directory)
	}
	if cfg.Stack != "demo" {
		t.Fatalf("expected stack directory demo, got %s", cfg.Stack)
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
