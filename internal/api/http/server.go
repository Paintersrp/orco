package httpapi

import (
	stdcontext "context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Paintersrp/orco/internal/api"
)

const (
	defaultAddr            = "127.0.0.1:7663"
	defaultReadHeader      = 5 * time.Second
	defaultShutdownTimeout = 5 * time.Second
)

// Config controls construction of the API server.
type Config struct {
	Addr              string
	Controller        api.Controller
	Listener          net.Listener
	ReadHeaderTimeout time.Duration
	ShutdownTimeout   time.Duration
}

// Server wraps an http.Server exposing orchestration controls.
type Server struct {
	ctrl            api.Controller
	srv             *http.Server
	listener        net.Listener
	shutdownTimeout time.Duration
}

// NewServer constructs a Server with sane defaults.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Controller == nil {
		return nil, fmt.Errorf("controller is required")
	}
	addr := normalizeAddr(cfg.Addr)
	mux := http.NewServeMux()
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
	}
	if srv.ReadHeaderTimeout == 0 {
		srv.ReadHeaderTimeout = defaultReadHeader
	}
	server := &Server{
		ctrl:            cfg.Controller,
		srv:             srv,
		listener:        cfg.Listener,
		shutdownTimeout: cfg.ShutdownTimeout,
	}
	if server.shutdownTimeout == 0 {
		server.shutdownTimeout = defaultShutdownTimeout
	}
	server.registerRoutes(mux)
	return server, nil
}

// Run starts serving until the provided context is cancelled.
func (s *Server) Run(ctx stdcontext.Context) error {
	if ctx == nil {
		ctx = stdcontext.Background()
	}
	errCh := make(chan error, 1)
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := stdcontext.WithTimeout(stdcontext.Background(), s.shutdownTimeout)
			defer cancel()
			_ = s.srv.Shutdown(shutdownCtx)
		case <-stop:
		}
	}()

	go func() {
		var err error
		if s.listener != nil {
			err = s.srv.Serve(s.listener)
		} else {
			err = s.srv.ListenAndServe()
		}
		errCh <- err
	}()

	err := <-errCh
	close(stop)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Addr returns the listen address.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.srv.Addr
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/status", s.handleStatus)
	mux.HandleFunc("/api/v1/restart/", s.handleRestart)
	mux.HandleFunc("/api/v1/apply", s.handleApply)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.methodNotAllowed(w, http.MethodGet)
		return
	}
	result, err := s.ctrl.Status(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.methodNotAllowed(w, http.MethodPost)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/restart/")
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, "/") {
		s.writeErrorWithDetails(w, fmt.Errorf("%w: invalid service path", api.ErrUnknownService), map[string]any{"service": name})
		return
	}
	result, err := s.ctrl.RestartService(r.Context(), name)
	if err != nil {
		s.writeErrorWithDetails(w, err, map[string]any{"service": name})
		return
	}
	payload := map[string]any{
		"restart": result,
	}
	s.writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.methodNotAllowed(w, http.MethodPost)
		return
	}
	result, err := s.ctrl.Apply(r.Context())
	if err != nil {
		s.writeError(w, err)
		return
	}
	payload := map[string]any{
		"apply": result,
	}
	if status, statusErr := s.ctrl.Status(r.Context()); statusErr == nil {
		payload["status"] = status
	}
	s.writeJSON(w, http.StatusOK, payload)
}

func (s *Server) methodNotAllowed(w http.ResponseWriter, method string) {
	w.Header().Set("Allow", method)
	s.writeJSON(w, http.StatusMethodNotAllowed, errorBody{
		Code:    "method_not_allowed",
		Message: fmt.Sprintf("method %s not allowed", method),
	})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(payload)
}

type errorBody struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Details interface{} `json:"details"`
}

func (s *Server) writeError(w http.ResponseWriter, err error) {
	s.writeErrorWithDetails(w, err, nil)
}

func (s *Server) writeErrorWithDetails(w http.ResponseWriter, err error, extra map[string]any) {
	status, code := classifyError(err)
	details := map[string]any{
		"timestamp": time.Now().UTC(),
	}
	for k, v := range extra {
		details[k] = v
	}
	body := errorBody{
		Code:    code,
		Message: err.Error(),
		Details: details,
	}
	s.writeJSON(w, status, body)
}

func classifyError(err error) (int, string) {
	switch {
	case errors.Is(err, stdcontext.Canceled):
		return 499, "context_canceled"
	case errors.Is(err, api.ErrUnknownService):
		return http.StatusNotFound, "unknown_service"
	case errors.Is(err, api.ErrNoActiveDeployment):
		return http.StatusConflict, "no_active_deployment"
	case errors.Is(err, api.ErrStackMismatch):
		return http.StatusConflict, "stack_mismatch"
	case errors.Is(err, api.ErrServiceNotRunning):
		return http.StatusConflict, "service_not_running"
	case errors.Is(err, api.ErrNoStoredStackSpec):
		return http.StatusConflict, "missing_stack_spec"
	case errors.Is(err, api.ErrUnsupportedChange):
		return http.StatusBadRequest, "unsupported_change"
	case errors.Is(err, api.ErrReadinessTimeout):
		return http.StatusGatewayTimeout, "readiness_timeout"
	default:
		return http.StatusInternalServerError, "internal_error"
	}
}

func normalizeAddr(addr string) string {
	if strings.TrimSpace(addr) == "" {
		return defaultAddr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// If parsing failed, trust caller.
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
