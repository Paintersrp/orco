package cli

import (
	"bytes"
	stdcontext "context"
	"errors"
	"net"
	"strings"
	"testing"

	apihttp "github.com/Paintersrp/orco/internal/api/http"
	"github.com/Paintersrp/orco/internal/engine"
	"github.com/Paintersrp/orco/internal/runtime"
)

func TestServeCommandReportsAPIServerError(t *testing.T) {
	t.Parallel()

	stackPath := writeStackFile(t, `version: "0.1"
stack:
  name: demo
  workdir: .
services:
  api:
    runtime: process
    command: ["sleep", "0"]
`)

	rt := newMockRuntime()
	ctx := &context{
		stackFile:    &stackPath,
		orchestrator: engine.NewOrchestrator(runtime.Registry{"process": rt}),
	}

	cmd := newServeCmd(ctx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	runCtx, cancel := stdcontext.WithCancel(stdcontext.Background())
	defer cancel()
	cmd.SetContext(runCtx)

	t.Setenv("ORCO_ENABLE_API", "true")

	startErr := errors.New("serve failure")
	origNewAPIServer := newAPIServer
	t.Cleanup(func() {
		newAPIServer = origNewAPIServer
	})
	newAPIServer = func(cfg apihttp.Config) (*apihttp.Server, error) {
		cfg.Listener = &failingListener{addr: staticAddr("127.0.0.1:0"), err: startErr}
		return apihttp.NewServer(cfg)
	}

	err := cmd.Execute()
	if !errors.Is(err, startErr) {
		t.Fatalf("expected serve error %v, got %v (stderr: %s)", startErr, err, stderr.String())
	}
	if strings.Contains(stdout.String(), "Control API listening") {
		t.Fatalf("expected no API startup message, got stdout: %s", stdout.String())
	}
}

type failingListener struct {
	addr net.Addr
	err  error
}

func (l *failingListener) Accept() (net.Conn, error) {
	return nil, l.err
}

func (l *failingListener) Close() error {
	return nil
}

func (l *failingListener) Addr() net.Addr {
	return l.addr
}

type staticAddr string

func (a staticAddr) Network() string { return "tcp" }

func (a staticAddr) String() string { return string(a) }
