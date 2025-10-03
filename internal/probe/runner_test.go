package probe

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestRunnerWatchHTTPTransitions(t *testing.T) {
	var phase atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if phase.Load() == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	interval := 20 * time.Millisecond
	grace := 40 * time.Millisecond
	health := &stack.Health{
		GracePeriod:      stack.Duration{Duration: grace},
		Interval:         stack.Duration{Duration: interval},
		Timeout:          stack.Duration{Duration: 200 * time.Millisecond},
		FailureThreshold: 2,
		SuccessThreshold: 2,
		HTTP: &stack.HTTPProbe{
			URL:          server.URL,
			ExpectStatus: []int{http.StatusOK},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	states := make(chan State, 8)
	start := time.Now()
	go NewRunner(health).Watch(ctx, states)

	ensureNoEvent(t, states, grace/2)

	ready := expectState(t, states, StatusReady, 750*time.Millisecond)
	if ready.Err != nil {
		t.Fatalf("unexpected ready error: %v", ready.Err)
	}
	if since := time.Since(start); since < grace+interval {
		t.Fatalf("ready reported too early: %v", since)
	}

	phase.Store(1)
	ensureNoEvent(t, states, interval+interval/2)

	unready := expectState(t, states, StatusUnready, 750*time.Millisecond)
	if unready.Err == nil {
		t.Fatalf("expected unready event to surface an error")
	}

	phase.Store(0)
	ensureNoEvent(t, states, interval+interval/2)

	readyAgain := expectState(t, states, StatusReady, 750*time.Millisecond)
	if readyAgain.Err != nil {
		t.Fatalf("unexpected ready error on recovery: %v", readyAgain.Err)
	}
}

func TestRunnerWatchCommandTransitions(t *testing.T) {
	shell := "/bin/sh"
	if runtime.GOOS == "windows" {
		t.Skip("shell not available on windows test environment")
	}

	tempDir := t.TempDir()
	probeFile := filepath.Join(tempDir, "probe")
	if err := os.WriteFile(probeFile, []byte{}, 0o644); err != nil {
		t.Fatalf("create probe file: %v", err)
	}

	interval := 15 * time.Millisecond
	health := &stack.Health{
		GracePeriod:      stack.Duration{},
		Interval:         stack.Duration{Duration: interval},
		Timeout:          stack.Duration{Duration: 200 * time.Millisecond},
		FailureThreshold: 2,
		SuccessThreshold: 1,
		Command: &stack.CommandProbe{
			Command: []string{shell, "-c", "test -f " + probeFile},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	states := make(chan State, 8)
	go NewRunner(health).Watch(ctx, states)

	ready := expectState(t, states, StatusReady, 500*time.Millisecond)
	if ready.Err != nil {
		t.Fatalf("unexpected ready error: %v", ready.Err)
	}

	if err := os.Remove(probeFile); err != nil {
		t.Fatalf("remove probe file: %v", err)
	}

	ensureNoEvent(t, states, interval+interval/2)

	unready := expectState(t, states, StatusUnready, 500*time.Millisecond)
	if unready.Err == nil {
		t.Fatalf("expected unready event to contain an error")
	}

	if err := os.WriteFile(probeFile, []byte{}, 0o644); err != nil {
		t.Fatalf("recreate probe file: %v", err)
	}

	readyAgain := expectState(t, states, StatusReady, 500*time.Millisecond)
	if readyAgain.Err != nil {
		t.Fatalf("unexpected ready error on recovery: %v", readyAgain.Err)
	}
}

func TestRunnerWatchTCPTransitions(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	interval := 15 * time.Millisecond
	health := &stack.Health{
		GracePeriod:      stack.Duration{},
		Interval:         stack.Duration{Duration: interval},
		Timeout:          stack.Duration{Duration: 200 * time.Millisecond},
		FailureThreshold: 3,
		SuccessThreshold: 1,
		TCP: &stack.TCPProbe{
			Address: ln.Addr().String(),
		},
	}

	runner := NewRunner(health)
	var failing atomic.Bool
	runner.dialer = func(dialCtx context.Context, network, address string) (net.Conn, error) {
		if failing.Load() {
			return nil, errors.New("dial failure")
		}
		return (&net.Dialer{}).DialContext(dialCtx, network, address)
	}

	states := make(chan State, 8)
	go runner.Watch(ctx, states)

	ready := expectState(t, states, StatusReady, 500*time.Millisecond)
	if ready.Err != nil {
		t.Fatalf("unexpected ready error: %v", ready.Err)
	}

	failing.Store(true)
	ensureNoEvent(t, states, 2*interval)

	unready := expectState(t, states, StatusUnready, 750*time.Millisecond)
	if unready.Err == nil {
		t.Fatalf("expected unready event to include error")
	}

	failing.Store(false)
	readyAgain := expectState(t, states, StatusReady, 750*time.Millisecond)
	if readyAgain.Err != nil {
		t.Fatalf("unexpected ready error on recovery: %v", readyAgain.Err)
	}
}

func expectState(t *testing.T, states <-chan State, status Status, timeout time.Duration) State {
	t.Helper()
	select {
	case state := <-states:
		if state.Status != status {
			t.Fatalf("expected state %s, got %s", status, state.Status)
		}
		return state
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for state %s", status)
		return State{}
	}
}

func ensureNoEvent(t *testing.T, states <-chan State, duration time.Duration) {
	t.Helper()
	select {
	case state := <-states:
		t.Fatalf("unexpected state %s during quiet period", state.Status)
	case <-time.After(duration):
	}
}
