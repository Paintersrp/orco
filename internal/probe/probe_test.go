package probe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/stack"
)

func TestWatchHTTPTransitions(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(false)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	spec := &stack.Health{
		GracePeriod:      stack.Duration{Duration: 60 * time.Millisecond},
		Interval:         stack.Duration{Duration: 15 * time.Millisecond},
		Timeout:          stack.Duration{Duration: 200 * time.Millisecond},
		FailureThreshold: 2,
		SuccessThreshold: 1,
		HTTP: &stack.HTTPProbe{
			URL: server.URL,
		},
	}

	prober, err := New(spec)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		time.Sleep(30 * time.Millisecond)
		healthy.Store(true)
	}()

	events := Watch(ctx, prober, spec, nil)

	ensureNoEvent(t, events, 40*time.Millisecond)

	ready := expectEvent(t, events, StatusReady, time.Second)
	if ready.Err != nil {
		t.Fatalf("expected ready without error, got %v", ready.Err)
	}
	if ready.Reason != "" {
		t.Fatalf("expected empty ready reason, got %q", ready.Reason)
	}

	healthy.Store(false)
	ensureNoEvent(t, events, spec.Interval.Duration+spec.Interval.Duration/2)

	unready := expectEvent(t, events, StatusUnready, time.Second)
	if unready.Status != StatusUnready {
		t.Fatalf("expected unready, got %s", unready.Status)
	}
	if !strings.HasPrefix(unready.Reason, "status=503") {
		t.Fatalf("expected http status reason, got %q", unready.Reason)
	}
}

func TestWatchTCPClosedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	spec := &stack.Health{
		Interval:         stack.Duration{Duration: 10 * time.Millisecond},
		Timeout:          stack.Duration{Duration: 50 * time.Millisecond},
		FailureThreshold: 2,
		SuccessThreshold: 1,
		TCP: &stack.TCPProbe{
			Address: addr,
		},
	}

	prober, err := New(spec)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	events := Watch(ctx, prober, spec, nil)
	event := expectEvent(t, events, StatusUnready, time.Second)
	if event.Reason == "" {
		t.Fatalf("expected failure reason for tcp probe")
	}
	if !strings.Contains(event.Reason, "dial") {
		t.Fatalf("expected dial failure, got %q", event.Reason)
	}

	ensureNoEvent(t, events, 50*time.Millisecond)
}

func TestWatchCommandProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell not available on windows test environment")
	}

	shell := "/bin/sh"
	spec := &stack.Health{
		Interval:         stack.Duration{Duration: 15 * time.Millisecond},
		Timeout:          stack.Duration{Duration: 500 * time.Millisecond},
		FailureThreshold: 1,
		SuccessThreshold: 1,
		Command: &stack.CommandProbe{
			Command: []string{shell, "-c", "exit 1"},
		},
	}

	prober, err := New(spec)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	events := Watch(ctx, prober, spec, nil)
	failure := expectEvent(t, events, StatusUnready, time.Second)
	if failure.Reason != "exit 1" {
		t.Fatalf("expected exit reason, got %q", failure.Reason)
	}

	t.Run("timeout", func(t *testing.T) {
		timeoutSpec := &stack.Health{
			Interval:         stack.Duration{},
			Timeout:          stack.Duration{Duration: 50 * time.Millisecond},
			FailureThreshold: 1,
			SuccessThreshold: 1,
		}
		timeoutProber := proberFunc(func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})

		timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), time.Second)
		defer timeoutCancel()

		start := time.Now()
		timeoutEvents := Watch(timeoutCtx, timeoutProber, timeoutSpec, nil)
		timeoutEvent := expectEvent(t, timeoutEvents, StatusUnready, time.Second)
		if !strings.Contains(timeoutEvent.Reason, "timeout") {
			t.Fatalf("expected timeout reason, got %q", timeoutEvent.Reason)
		}
		if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
			t.Fatalf("probe exceeded timeout budget: %v", elapsed)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		cancelSpec := &stack.Health{
			Interval:         stack.Duration{Duration: 10 * time.Millisecond},
			Timeout:          stack.Duration{Duration: 500 * time.Millisecond},
			FailureThreshold: 1,
			SuccessThreshold: 1,
		}
		cancelProber := proberFunc(func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		})

		cancelCtx, cancelFn := context.WithCancel(context.Background())
		cancelEvents := Watch(cancelCtx, cancelProber, cancelSpec, nil)
		cancelFn()
		select {
		case _, ok := <-cancelEvents:
			if ok {
				t.Fatalf("expected channel to close after cancellation")
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("watcher did not close on cancellation")
		}
	})
}

func TestNewCommandRequiresArguments(t *testing.T) {
	_, err := newCommandProber(&stack.CommandProbe{})
	if err == nil {
		t.Fatalf("expected error for missing command")
	}
	if !strings.Contains(err.Error(), "requires") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type proberFunc func(context.Context) error

func (p proberFunc) Probe(ctx context.Context) error {
	return p(ctx)
}

func expectEvent(t *testing.T, events <-chan Event, status Status, timeout time.Duration) Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatalf("events channel closed while waiting for %s", status)
		}
		if event.Status != status {
			t.Fatalf("expected status %s, got %s", status, event.Status)
		}
		return event
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for status %s", status)
	}
	return Event{}
}

func ensureNoEvent(t *testing.T, events <-chan Event, duration time.Duration) {
	t.Helper()
	select {
	case event, ok := <-events:
		if ok {
			t.Fatalf("unexpected event %s during quiet period", event.Status)
		}
		t.Fatalf("events channel closed unexpectedly")
	case <-time.After(duration):
	}
}
