package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Paintersrp/orco/internal/probe"
	"github.com/Paintersrp/orco/internal/runtime"
	"github.com/Paintersrp/orco/internal/stack"
)

const (
	// RuntimeName identifies the proxy runtime in stack manifests.
	RuntimeName = "proxy"

	listenAddrEnv     = "ORCO_PROXY_LISTEN"
	targetHostEnv     = "ORCO_PROXY_TARGET_HOST"
	defaultListenAddr = "127.0.0.1:8080"
	defaultTargetHost = "127.0.0.1"
)

// New constructs a runtime that launches an in-process HTTP reverse proxy.
func New() runtime.Runtime {
	return &runtimeImpl{}
}

type runtimeImpl struct{}

// Start launches the proxy server according to the supplied specification.
func (r *runtimeImpl) Start(ctx context.Context, spec runtime.StartSpec) (runtime.Handle, error) {
	if spec.Proxy == nil {
		return nil, fmt.Errorf("proxy runtime for service %s requires proxy configuration", spec.Name)
	}

	cfg := spec.Proxy.Clone()
	if cfg == nil {
		return nil, fmt.Errorf("proxy runtime for service %s requires proxy configuration", spec.Name)
	}

	listenAddr := defaultListenAddr
	explicitListen := false
	if spec.Env != nil {
		if addr := strings.TrimSpace(spec.Env[listenAddrEnv]); addr != "" {
			listenAddr = addr
			explicitListen = true
		}
	}
	if !explicitListen {
		if envAddr := strings.TrimSpace(os.Getenv(listenAddrEnv)); envAddr != "" {
			listenAddr = envAddr
		}
	}

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("proxy listen %s: %w", listenAddr, err)
	}

	select {
	case <-ctx.Done():
		_ = listener.Close()
		return nil, ctx.Err()
	default:
	}

	targetHost := defaultTargetHost
	explicitTarget := false
	if spec.Env != nil {
		if host := strings.TrimSpace(spec.Env[targetHostEnv]); host != "" {
			targetHost = host
			explicitTarget = true
		}
	}
	if !explicitTarget {
		if envHost := strings.TrimSpace(os.Getenv(targetHostEnv)); envHost != "" {
			targetHost = envHost
		}
	}

	handler, err := newProxyHandler(cfg, targetHost)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}

	srv := &http.Server{Handler: handler}
	inst := &proxyInstance{
		server:   srv,
		listener: listener,
		ready:    make(chan struct{}),
		done:     make(chan struct{}),
		logs:     make(chan runtime.LogEntry, 1),
	}

	inst.logs <- runtime.LogEntry{
		Message:   fmt.Sprintf("proxy listening on %s", listener.Addr().String()),
		Source:    runtime.LogSourceSystem,
		Level:     "info",
		Timestamp: time.Now(),
	}

	go inst.serve()

	close(inst.ready)

	go func() {
		<-ctx.Done()
		_ = inst.server.Shutdown(context.Background())
	}()

	return inst, nil
}

type proxyInstance struct {
	server   *http.Server
	listener net.Listener

	ready chan struct{}
	done  chan struct{}

	stopOnce sync.Once
	waitErr  error

	logs chan runtime.LogEntry
}

func (p *proxyInstance) WaitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.ready:
		return nil
	}
}

func (p *proxyInstance) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return p.waitErr
	}
}

func (p *proxyInstance) Health() <-chan probe.State {
	return nil
}

func (p *proxyInstance) Stop(ctx context.Context) error {
	var err error
	p.stopOnce.Do(func() {
		err = p.server.Shutdown(ctx)
	})
	return err
}

func (p *proxyInstance) Kill(ctx context.Context) error {
	var err error
	p.stopOnce.Do(func() {
		err = p.server.Close()
	})
	return err
}

func (p *proxyInstance) Logs(ctx context.Context) (<-chan runtime.LogEntry, error) {
	if ctx == nil {
		return p.logs, nil
	}

	out := make(chan runtime.LogEntry, cap(p.logs))
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case entry, ok := <-p.logs:
				if !ok {
					return
				}
				select {
				case out <- entry:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

func (p *proxyInstance) serve() {
	err := p.server.Serve(p.listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		p.waitErr = err
	}
	close(p.logs)
	close(p.done)
}

type proxyHandler struct {
	routes []*routeHandler
	assets *assetHandler
}

func newProxyHandler(cfg *stack.Proxy, targetHost string) (http.Handler, error) {
	if cfg == nil {
		return nil, errors.New("proxy configuration is nil")
	}

	routes := make([]*routeHandler, 0, len(cfg.Routes))
	for idx, rt := range cfg.Routes {
		if rt == nil {
			continue
		}
		if rt.Port <= 0 {
			return nil, fmt.Errorf("proxy route %d: invalid port %d", idx, rt.Port)
		}
		route, err := buildRouteHandler(rt, targetHost)
		if err != nil {
			return nil, fmt.Errorf("proxy route %d: %w", idx, err)
		}
		routes = append(routes, route)
	}

	var assets *assetHandler
	if cfg.Assets != nil && (cfg.Assets.Directory != "" || cfg.Assets.Index != "") {
		assets = &assetHandler{directory: cfg.Assets.Directory, index: cfg.Assets.Index}
	}

	return &proxyHandler{routes: routes, assets: assets}, nil
}

func (h *proxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for _, route := range h.routes {
		if route.match(r) {
			route.serve(w, r)
			return
		}
	}
	if h.assets != nil && h.assets.serve(w, r) {
		return
	}
	http.NotFound(w, r)
}

type routeHandler struct {
	pathPrefix string
	headers    map[string]string
	proxy      *httputil.ReverseProxy
	strip      bool
}

func buildRouteHandler(rt *stack.ProxyRoute, host string) (*routeHandler, error) {
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("%s:%d", host, rt.Port)}
	proxy := httputil.NewSingleHostReverseProxy(target)
	prefix := rt.PathPrefix
	strip := rt.StripPathPrefix
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
		if strip && prefix != "" && strings.HasPrefix(req.URL.Path, prefix) {
			trimmed := strings.TrimPrefix(req.URL.Path, prefix)
			if trimmed == "" {
				trimmed = "/"
			} else if !strings.HasPrefix(trimmed, "/") {
				trimmed = "/" + trimmed
			}
			req.URL.Path = trimmed
			if raw := req.URL.RawPath; raw != "" && strings.HasPrefix(raw, prefix) {
				trimmedRaw := strings.TrimPrefix(raw, prefix)
				if trimmedRaw == "" {
					trimmedRaw = "/"
				} else if !strings.HasPrefix(trimmedRaw, "/") {
					trimmedRaw = "/" + trimmedRaw
				}
				req.URL.RawPath = trimmedRaw
			}
		}
	}
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		http.Error(rw, fmt.Sprintf("proxy error: %v", err), http.StatusBadGateway)
	}

	headers := make(map[string]string, len(rt.Headers))
	for k, v := range rt.Headers {
		headers[textproto.CanonicalMIMEHeaderKey(k)] = v
	}

	return &routeHandler{
		pathPrefix: prefix,
		headers:    headers,
		proxy:      proxy,
		strip:      strip,
	}, nil
}

func (r *routeHandler) match(req *http.Request) bool {
	if r.pathPrefix != "" && !strings.HasPrefix(req.URL.Path, r.pathPrefix) {
		return false
	}
	if len(r.headers) == 0 {
		return true
	}
	for key, value := range r.headers {
		if req.Header.Get(key) != value {
			return false
		}
	}
	return true
}

func (r *routeHandler) serve(w http.ResponseWriter, req *http.Request) {
	r.proxy.ServeHTTP(w, req)
}

type assetHandler struct {
	directory string
	index     string
}

func (a *assetHandler) serve(w http.ResponseWriter, r *http.Request) bool {
	if a.directory == "" {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}

	cleaned := filepath.Clean(r.URL.Path)
	if strings.Contains(cleaned, "..") {
		http.NotFound(w, r)
		return true
	}

	rel := strings.TrimPrefix(cleaned, "/")
	rel = strings.TrimPrefix(rel, string(os.PathSeparator))
	target := filepath.Join(a.directory, filepath.FromSlash(rel))
	if info, err := os.Stat(target); err == nil && info.Mode().IsRegular() {
		http.ServeFile(w, r, target)
		return true
	}

	if a.index != "" {
		if info, err := os.Stat(a.index); err == nil && info.Mode().IsRegular() {
			http.ServeFile(w, r, a.index)
			return true
		}
	}

	return false
}
