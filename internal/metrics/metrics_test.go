package metrics_test

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Paintersrp/orco/internal/metrics"
)

func TestRegistryExposesMetrics(t *testing.T) {
	t.Helper()
	service := "metrics_test_service"

	metrics.EmitBuildInfo()
	metrics.SetServiceReady(service, true)
	metrics.AddServiceRestarts(service, 2)
	metrics.ObserveProbeLatency(service, 150*time.Millisecond)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	promhttp.HandlerFor(metrics.Registry(), promhttp.HandlerOpts{}).ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("unexpected status code from metrics handler: %d", rec.Code)
	}

	body := rec.Body.String()
	readyLine := fmt.Sprintf("orco_service_ready{service=\"%s\"} 1", service)
	if !strings.Contains(body, readyLine) {
		t.Fatalf("expected readiness metric line %q in body:\n%s", readyLine, body)
	}

	restartsLine := fmt.Sprintf("orco_service_restarts_total{service=\"%s\"} 2", service)
	if !strings.Contains(body, restartsLine) {
		t.Fatalf("expected restart metric line %q in body:\n%s", restartsLine, body)
	}

	if !strings.Contains(body, "orco_build_info{") {
		t.Fatalf("expected build info metric in body:\n%s", body)
	}
	if !strings.Contains(body, "go_version=") {
		t.Fatalf("expected go_version label on build info metric:\n%s", body)
	}

	latencySumLine := fmt.Sprintf("orco_probe_latency_seconds_sum{service=\"%s\"}", service)
	if !strings.Contains(body, latencySumLine) {
		t.Fatalf("expected latency sum metric line containing %q in body:\n%s", latencySumLine, body)
	}
	latencyCountLine := fmt.Sprintf("orco_probe_latency_seconds_count{service=\"%s\"} 1", service)
	if !strings.Contains(body, latencyCountLine) {
		t.Fatalf("expected latency count metric line %q in body:\n%s", latencyCountLine, body)
	}
	quantileOptionA := fmt.Sprintf("orco_probe_latency_seconds{service=\"%s\",quantile=\"0.5\"}", service)
	quantileOptionB := fmt.Sprintf("orco_probe_latency_seconds{quantile=\"0.5\",service=\"%s\"}", service)
	if !strings.Contains(body, quantileOptionA) && !strings.Contains(body, quantileOptionB) {
		t.Fatalf("expected latency quantile metric containing either %q or %q in body:\n%s", quantileOptionA, quantileOptionB, body)
	}
}
