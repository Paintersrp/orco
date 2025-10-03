package probe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/example/orco/internal/stack"
)

func TestRunnerHTTPProbeSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	health := &stack.Health{
		GracePeriod:      stack.Duration{},
		Interval:         stack.Duration{Duration: 10 * time.Millisecond},
		Timeout:          stack.Duration{Duration: 100 * time.Millisecond},
		FailureThreshold: 3,
		SuccessThreshold: 1,
		HTTP: &stack.HTTPProbe{
			URL:          server.URL,
			ExpectStatus: []int{http.StatusOK},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := NewRunner(health).Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestRunnerHTTPProbeFailureThreshold(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	health := &stack.Health{
		GracePeriod:      stack.Duration{},
		Interval:         stack.Duration{Duration: 10 * time.Millisecond},
		Timeout:          stack.Duration{Duration: 50 * time.Millisecond},
		FailureThreshold: 2,
		SuccessThreshold: 1,
		HTTP: &stack.HTTPProbe{
			URL: server.URL,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := NewRunner(health).Run(ctx); err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestRunnerHTTPProbeSuccessThreshold(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&hits, 1)
		if attempt < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	health := &stack.Health{
		GracePeriod:      stack.Duration{},
		Interval:         stack.Duration{Duration: 10 * time.Millisecond},
		Timeout:          stack.Duration{Duration: 50 * time.Millisecond},
		FailureThreshold: 3,
		SuccessThreshold: 2,
		HTTP: &stack.HTTPProbe{
			URL: server.URL,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := NewRunner(health).Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestRunnerTCPProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	health := &stack.Health{
		GracePeriod:      stack.Duration{},
		Interval:         stack.Duration{Duration: 10 * time.Millisecond},
		Timeout:          stack.Duration{Duration: 50 * time.Millisecond},
		FailureThreshold: 3,
		SuccessThreshold: 1,
		TCP: &stack.TCPProbe{
			Address: ln.Addr().String(),
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := NewRunner(health).Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestRunnerCommandProbe(t *testing.T) {
	shell := "/bin/sh"
	if runtime.GOOS == "windows" {
		t.Skip("shell not available on windows test environment")
	}

	health := &stack.Health{
		GracePeriod:      stack.Duration{},
		Interval:         stack.Duration{Duration: 10 * time.Millisecond},
		Timeout:          stack.Duration{Duration: 100 * time.Millisecond},
		FailureThreshold: 3,
		SuccessThreshold: 1,
		Command: &stack.CommandProbe{
			Command: []string{shell, "-c", "exit 0"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := NewRunner(health).Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestRunnerCommandProbeFailureThreshold(t *testing.T) {
	shell := "/bin/sh"
	if runtime.GOOS == "windows" {
		t.Skip("shell not available on windows test environment")
	}

	health := &stack.Health{
		GracePeriod:      stack.Duration{},
		Interval:         stack.Duration{Duration: 10 * time.Millisecond},
		Timeout:          stack.Duration{Duration: 100 * time.Millisecond},
		FailureThreshold: 2,
		SuccessThreshold: 1,
		Command: &stack.CommandProbe{
			Command: []string{shell, "-c", "exit 1"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := NewRunner(health).Run(ctx); err == nil {
		t.Fatalf("expected failure, got nil")
	}
}

func TestRunnerCommandProbeTimeoutOverride(t *testing.T) {
	shell := "/bin/sh"
	if runtime.GOOS == "windows" {
		t.Skip("shell not available on windows test environment")
	}

	health := &stack.Health{
		GracePeriod:      stack.Duration{},
		Interval:         stack.Duration{Duration: 10 * time.Millisecond},
		Timeout:          stack.Duration{Duration: time.Second},
		FailureThreshold: 2,
		SuccessThreshold: 1,
		Command: &stack.CommandProbe{
			Command: []string{shell, "-c", "sleep 0.2"},
			Timeout: stack.Duration{Duration: 50 * time.Millisecond},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	if err := NewRunner(health).Run(ctx); err == nil {
		t.Fatalf("expected timeout error")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("probe did not respect command timeout override")
	}
}
