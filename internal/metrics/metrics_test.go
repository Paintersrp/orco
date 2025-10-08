package metrics_test

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Paintersrp/orco/internal/metrics"
)

func TestRegistryExposesMetrics(t *testing.T) {
	t.Helper()
	service := "metrics_test_service"

	metrics.EmitBuildInfo()
	metrics.SetServiceReady(service, true)
	metrics.AddServiceRestarts(service, 2)

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
}
