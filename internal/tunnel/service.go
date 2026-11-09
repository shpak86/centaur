package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/shpak86/centaur/internal/metrics"
)

// requestWriteTimeout limits writing the request into a tunnel: if the connector
// is not reading (a dead tunnel), we do not wait forever. Reading the response
// has no deadline, so as not to cut short long streaming responses from Ollama.
const requestWriteTimeout = 30 * time.Second

// sweepInterval is the period of the background sweep of dead tunnels from the
// registry. The hot path is protected by the probe in Acquire, so the interval
// affects only the honesty of the pool state metric and the lifetime of dead
// connection descriptors.
const sweepInterval = 5 * time.Second

// pastDeadline is a deadline in the past: it unblocks a blocked read.
var pastDeadline = time.Unix(1, 0)

// errNoTunnel is returned when the pool has no free tunnels.
var errNoTunnel = errors.New("no free tunnel")

// errBadRequestBody is returned when the body of a model endpoint request cannot
// be parsed as JSON: the proxy answers 400 without occupying a tunnel.
var errBadRequestBody = errors.New("bad request body")

// Service is the gateway side of the tunnel pool: it serves the tunnel
// registration API, accepts tunnel connections, and proxies client requests to
// them.
type Service struct {
	apiServer    *ApiServer
	tunnelServer *TunnelServer
	registry     *Registry
	proxy        *httputil.ReverseProxy
	metrics      *metrics.Metrics
	ctx          context.Context
	cancel       context.CancelFunc
	// sweepEvery is the dead-tunnel sweep period; it equals sweepInterval in
	// normal operation and is shortened in tests.
	sweepEvery time.Duration
}

// NewService creates a tunnel-pool service listening on apiAddr (the
// registration HTTP API) and tunnelAddr (the TCP pool). apiKeys is the list of
// accepted connector API keys.
func NewService(apiAddr, tunnelAddr string, apiKeys []string) *Service {
	httpOps := httpServerOps{
		Address:           apiAddr,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	tunnelOps := tunnelServerOps{
		Address:           tunnelAddr,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	service := Service{sweepEvery: sweepInterval}
	service.ctx, service.cancel = context.WithCancel(context.Background())
	service.metrics = metrics.New()
	service.registry = NewRegistry(service.metrics)
	authManager := NewAuthManager(apiKeys)
	service.apiServer = NewApiServer(httpOps, authManager, service.registry)
	service.tunnelServer = NewTunnelServer(tunnelOps, service.registry)
	service.proxy = newTunnelProxy(service.registry)
	return &service
}

// newTunnelProxy builds the reverse proxy that sends a request into a tunnel.
// httputil.ReverseProxy parses the incoming request and re-serializes it itself
// (streaming the body), so neither hijacking the client connection nor manual
// HTTP parsing is needed.
func newTunnelProxy(registry *Registry) *httputil.ReverseProxy {
	// The address is fictitious: the transport always dials the tunnel socket
	// anyway, but ReverseProxy requires an absolute URL.
	target := &url.URL{Scheme: "http", Host: "tunnel"}

	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
		},

		// For Ollama's NDJSON streaming: flush every chunk immediately.
		FlushInterval: -1,

		Transport: &tunnelTransport{registry: registry},

		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, errNoTunnel) {
				slog.Debug("No free tunnel for request", "path", r.URL.Path)
				http.Error(w, "no free tunnel", http.StatusServiceUnavailable)
				return
			}
			if errors.Is(err, errBadRequestBody) {
				slog.Debug("Bad request body", "path", r.URL.Path, "error", err)
				http.Error(w, "bad request body", http.StatusBadRequest)
				return
			}
			slog.Error("Proxy request", "error", err)
			http.Error(w, "tunnel error", http.StatusBadGateway)
		},
	}
}

// tunnelTransport sends each request into a separate tunnel.
// Tunnels are single-use: after the response the connection is closed and the
// connector registers a new tunnel — so reconnection does not depend on what
// happens to the client connection.
type tunnelTransport struct {
	registry *Registry
}

func (t *tunnelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// The model name is needed for routing, so the body is read before Acquire:
	// a broken body yields 400 without occupying a tunnel. getModel puts the
	// body back into req.Body — req.Write writes it below.
	model, err := getModel(req)
	if err != nil {
		return nil, err
	}

	// Tunnels are single-use: if no matching tunnel is free right now, return
	// errNoTunnel (503) immediately — whether to retry is up to the client.
	tunnel, exists := t.registry.Acquire(model)
	if !exists {
		return nil, errNoTunnel
	}

	// The client may have disconnected while we wait for the response: close the
	// tunnel to unblock the read and avoid losing the connection.
	stopWatch := context.AfterFunc(req.Context(), func() {
		tunnel.Conn.SetReadDeadline(pastDeadline)
	})
	defer stopWatch()

	// Ask the connector to close the tunnel after the response (Connection:
	// close): the tunnel stays single-use and does not linger waiting for the
	// next request.
	req.Close = true

	tunnel.Conn.SetWriteDeadline(time.Now().Add(requestWriteTimeout))
	err = req.Write(tunnel.Conn)
	tunnel.Conn.SetWriteDeadline(time.Time{})
	if err != nil {
		tunnel.Close()
		return nil, fmt.Errorf("write request to tunnel: %w", err)
	}
	slog.Info(
		"tunnel request sent",
		"token", tunnel.Id,
		"remote", tunnel.Conn.RemoteAddr().String(),
		"method", req.Method,
		"path", req.URL.Path,
		"model", model,
	)

	resp, err := http.ReadResponse(bufio.NewReader(tunnel.Conn), req)
	if err != nil {
		tunnel.Close()
		return nil, fmt.Errorf("read response from tunnel: %w", err)
	}
	slog.Info("tunnel response received",
		"token", tunnel.Id,
		"remote", tunnel.Conn.RemoteAddr().String(),
		"status", resp.Status)

	// Release the tunnel together with the response body: by then it is read.
	resp.Body = &tunnelBody{ReadCloser: resp.Body, tunnel: tunnel, registry: t.registry}

	return resp, nil
}

// tunnelBody closes the single-use tunnel once the response body is fully read.
type tunnelBody struct {
	io.ReadCloser
	once     sync.Once
	tunnel   *Tunnel
	registry *Registry
}

func (b *tunnelBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() {
		b.tunnel.Close()
	})
	return err
}

// ListenAndServe starts the tunnel API and TCP servers and blocks until both
// stop.
func (s *Service) ListenAndServe() error {
	// The sweeper runs in its own goroutine rather than in the shared WaitGroup:
	// it exits on context cancellation (Stop or the defer below), and wg.Wait()
	// would otherwise wait for it forever.
	defer s.cancel()
	go s.sweepLoop(s.ctx)

	wg := sync.WaitGroup{}
	wg.Go(func() {
		err := s.apiServer.ListenAndServe()
		if err != nil {
			slog.Error("Tunnel API server", "error", err)
		}
	})
	wg.Go(func() {
		err := s.tunnelServer.ListenAndServe()
		if err != nil {
			slog.Error("Tunnel server", "error", err)
		}
	})
	wg.Wait()
	return nil
}

// sweepLoop periodically removes dead tunnels from the registry: otherwise a
// dead connector's tunnel stays "ready" in the pool and is handed to a client.
func (s *Service) sweepLoop(ctx context.Context) {
	ticker := time.NewTicker(s.sweepEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if dropped := s.registry.Sweep(); dropped > 0 {
				slog.Info("Sweep stale tunnels", "dropped", dropped)
			}
		}
	}
}

// Stop gracefully shuts down the tunnel API and TCP servers.
func (s *Service) Stop(ctx context.Context) error {
	// The sweeper first, then the servers: otherwise it would touch the registry
	// during shutdown.
	s.cancel()

	errs := make([]error, 0, 2)
	if err := s.apiServer.Stop(ctx); err != nil {
		errs = append(errs, fmt.Errorf("stop api server: %w", err))
	}
	if err := s.tunnelServer.Stop(); err != nil {
		errs = append(errs, fmt.Errorf("stop tunnel server: %w", err))
	}
	return errors.Join(errs...)
}

// MetricsHandler returns the gateway metrics in the Prometheus text format.
// It is served by a separate MetricsServer on metrics.address (GET /metrics),
// not by the client proxy on proxy.address.
func (s *Service) MetricsHandler() http.Handler {
	return s.metrics.Handler()
}

// Handle proxies a client request into a free tunnel.
// Only inference endpoints (modelPaths) are proxied: the gateway performs
// inference rather than managing a remote Ollama, so it does not proxy
// info/admin endpoints (tags, ps, show, pull, push, create, copy, delete,
// /v1/models) and answers 501 instead.
func (s *Service) Handle(w http.ResponseWriter, r *http.Request) {
	endpoint := endpointLabel(r.URL.Path)
	rec := &statusRecorder{ResponseWriter: w}
	start := time.Now()
	defer func() {
		s.metrics.RequestsTotal.WithLabelValues(endpoint, strconv.Itoa(rec.statusCode())).Inc()
		s.metrics.RequestDuration.WithLabelValues(endpoint).Observe(time.Since(start).Seconds())
	}()

	// GET / is the gateway liveness check: Ollama clients ping the root.
	// Answer it ourselves without going into a tunnel.
	if r.Method == http.MethodGet && r.URL.Path == "/" {
		rec.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if _, err := rec.Write([]byte("Centaur is running")); err != nil {
			slog.Debug("Write health response", "error", err)
		}
		return
	}

	if _, ok := modelPaths[r.URL.Path]; !ok {
		slog.Debug("Rejected non-inference request", "method", r.Method, "path", r.URL.Path)
		http.Error(rec, "centaur gateway proxies inference endpoints only", http.StatusNotImplemented)
		return
	}

	s.proxy.ServeHTTP(rec, r)
}

// endpointLabel bounds metric cardinality: inference endpoints use their own
// path, the root is used as is, and everything else collapses into "other".
func endpointLabel(path string) string {
	if path == "/" {
		return path
	}
	if _, ok := modelPaths[path]; ok {
		return path
	}
	return "other"
}

// statusRecorder records the HTTP response status for metrics. It implements
// http.Flusher and Unwrap: without them the wrapper would break NDJSON
// streaming, which ReverseProxy flushes via http.NewResponseController.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) Flush() {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (r *statusRecorder) statusCode() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}
