package httpapi

import (
	stdcontext "context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Paintersrp/orco/internal/api"
	"github.com/Paintersrp/orco/internal/metrics"
)

type testController struct{}

func (t *testController) Status(stdcontext.Context) (*api.StatusReport, error) {
	return nil, nil
}

func (t *testController) RestartService(stdcontext.Context, string) (*api.RestartResult, error) {
	return nil, nil
}

func (t *testController) Apply(stdcontext.Context) (*api.ApplyResult, error) {
	return nil, nil
}

func TestNewServerRejectsTypedNilController(t *testing.T) {
	var ctrl api.Controller = (*testController)(nil)
	_, err := NewServer(Config{Controller: ctrl})
	if err == nil {
		t.Fatalf("expected error when controller is typed nil")
	}
	if !strings.Contains(err.Error(), "testController") {
		t.Fatalf("expected error to describe typed nil controller, got %v", err)
	}
}

func TestNormalizeAddr(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":           defaultAddr,
		":80":        "127.0.0.1:80",
		"0.0.0.0:80": "0.0.0.0:80",
		"[::]:80":    "[::]:80",
		"host:9000":  "host:9000",
		"[::1]:443":  "[::1]:443",
	}

	for input, expected := range tests {
		input, expected := input, expected
		t.Run(fmt.Sprintf("%s->%s", input, expected), func(t *testing.T) {
			t.Parallel()
			if got := normalizeAddr(input); got != expected {
				t.Fatalf("normalizeAddr(%q)=%q, want %q", input, got, expected)
			}
		})
	}
}

func TestHandleStatus(t *testing.T) {
	ctrl := &mockController{
		statusFn: func(stdcontext.Context) (*api.StatusReport, error) {
			return &api.StatusReport{Stack: "demo", GeneratedAt: time.Unix(123, 0)}, nil
		},
	}
	server := newTestServer(t, ctrl)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	rec := httptest.NewRecorder()

	server.handleStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var body api.StatusReport
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed decoding response: %v", err)
	}
	if body.Stack != "demo" {
		t.Fatalf("expected stack 'demo', got %q", body.Stack)
	}
}

func TestHandleStatusError(t *testing.T) {
	ctrl := &mockController{
		statusFn: func(stdcontext.Context) (*api.StatusReport, error) {
			return nil, errors.New("boom")
		},
	}
	server := newTestServer(t, ctrl)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	rec := httptest.NewRecorder()

	server.handleStatus(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	var body errorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if body.Code != "internal_error" {
		t.Fatalf("expected internal_error code, got %q", body.Code)
	}
}

func TestHandleStatusMethodNotAllowed(t *testing.T) {
	ctrl := &mockController{}
	server := newTestServer(t, ctrl)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/status", nil)
	rec := httptest.NewRecorder()
	server.handleStatus(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("expected Allow header %q, got %q", http.MethodGet, allow)
	}
}

func TestHandleRestart(t *testing.T) {
	ctrl := &mockController{
		restartFn: func(_ stdcontext.Context, svc string) (*api.RestartResult, error) {
			if svc != "api" {
				t.Fatalf("unexpected service %q", svc)
			}
			return &api.RestartResult{Service: svc, Restarts: 1}, nil
		},
	}
	server := newTestServer(t, ctrl)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/restart/api", nil)
	rec := httptest.NewRecorder()
	server.handleRestart(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]api.RestartResult
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	result, ok := body["restart"]
	if !ok {
		t.Fatalf("expected restart field in response")
	}
	if result.Restarts != 1 {
		t.Fatalf("expected restart count 1, got %d", result.Restarts)
	}
}

func TestHandleRestartInvalidService(t *testing.T) {
	ctrl := &mockController{}
	server := newTestServer(t, ctrl)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/restart/", nil)
	rec := httptest.NewRecorder()
	server.handleRestart(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	var body errorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if body.Code != "unknown_service" {
		t.Fatalf("expected unknown_service code, got %q", body.Code)
	}
	details, ok := body.Details.(map[string]any)
	if !ok {
		t.Fatalf("expected map details, got %T", body.Details)
	}
	if _, ok := details["service"]; !ok {
		t.Fatalf("expected service key in details")
	}
	if _, ok := details["timestamp"]; !ok {
		t.Fatalf("expected timestamp key in details")
	}
}

func TestHandleApply(t *testing.T) {
	ctrl := &mockController{
		applyFn: func(stdcontext.Context) (*api.ApplyResult, error) {
			return &api.ApplyResult{Diff: "diff"}, nil
		},
		statusFn: func(stdcontext.Context) (*api.StatusReport, error) {
			return &api.StatusReport{Stack: "demo"}, nil
		},
	}
	server := newTestServer(t, ctrl)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/apply", nil)
	rec := httptest.NewRecorder()
	server.handleApply(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if _, ok := body["apply"].(map[string]any); !ok {
		t.Fatalf("expected apply result in body")
	}
	if status, ok := body["status"].(map[string]any); !ok || status["stack"] != "demo" {
		t.Fatalf("expected status stack demo, got %v", body["status"])
	}
}

func TestHandleApplyError(t *testing.T) {
	ctrl := &mockController{
		applyFn: func(stdcontext.Context) (*api.ApplyResult, error) {
			return nil, api.ErrNoActiveDeployment
		},
	}
	server := newTestServer(t, ctrl)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/apply", nil)
	rec := httptest.NewRecorder()
	server.handleApply(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
	var body errorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if body.Code != "no_active_deployment" {
		t.Fatalf("expected code no_active_deployment, got %q", body.Code)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	ctrl := &mockController{}
	server := newTestServer(t, ctrl)

	service := "http_metrics"
	metrics.SetServiceReady(service, true)
	metrics.ObserveProbeLatency(service, 200*time.Millisecond)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	server.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from metrics endpoint, got %d", rec.Code)
	}
	body := rec.Body.String()
	expected := fmt.Sprintf("orco_service_ready{service=\"%s\"} 1", service)
	if !strings.Contains(body, expected) {
		t.Fatalf("expected body to contain %q, got:\n%s", expected, body)
	}
	if !strings.Contains(body, fmt.Sprintf("orco_probe_latency_seconds_sum{service=\"%s\"}", service)) {
		t.Fatalf("expected metrics output to include latency sum for service %q, got:\n%s", service, body)
	}
	if !strings.Contains(body, fmt.Sprintf("orco_probe_latency_seconds_count{service=\"%s\"} 1", service)) {
		t.Fatalf("expected metrics output to include latency count for service %q, got:\n%s", service, body)
	}
	quantileA := fmt.Sprintf("orco_probe_latency_seconds{service=\"%s\",quantile=\"0.5\"}", service)
	quantileB := fmt.Sprintf("orco_probe_latency_seconds{quantile=\"0.5\",service=\"%s\"}", service)
	if !strings.Contains(body, quantileA) && !strings.Contains(body, quantileB) {
		t.Fatalf("expected metrics output to include latency quantile for service %q, got:\n%s", service, body)
	}
	if !strings.Contains(body, "orco_build_info{") {
		t.Fatalf("expected metrics output to include build info, got:\n%s", body)
	}
}

type mockController struct {
	statusFn  func(stdcontext.Context) (*api.StatusReport, error)
	restartFn func(stdcontext.Context, string) (*api.RestartResult, error)
	applyFn   func(stdcontext.Context) (*api.ApplyResult, error)
}

func (m *mockController) Status(ctx stdcontext.Context) (*api.StatusReport, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx)
	}
	return nil, nil
}

func (m *mockController) RestartService(ctx stdcontext.Context, svc string) (*api.RestartResult, error) {
	if m.restartFn != nil {
		return m.restartFn(ctx, svc)
	}
	return nil, nil
}

func (m *mockController) Apply(ctx stdcontext.Context) (*api.ApplyResult, error) {
	if m.applyFn != nil {
		return m.applyFn(ctx)
	}
	return nil, nil
}

func newTestServer(t *testing.T, ctrl api.Controller) *Server {
	t.Helper()
	server, err := NewServer(Config{Controller: ctrl})
	if err != nil {
		t.Fatalf("failed creating server: %v", err)
	}
	return server
}
