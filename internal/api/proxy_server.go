package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/shpak86/centaur/internal/tunnel"
)

const (
	readHeaderTimeout = 5 * time.Second
	idleTimeout       = 60 * time.Second
)

// ProxyServer is the gateway's client-facing HTTP server: it proxies client
// requests into tunnels and owns the tunnel pool listeners.
type ProxyServer struct {
	httpServer    *http.Server
	tunnelService *tunnel.Service
}

// NewProxyServer creates the client-facing proxy server bound to proxyAddress.
// poolApiAddress and poolAddress are the tunnel pool's registration API and TCP
// server; apiKeys are the accepted connector API keys.
func NewProxyServer(proxyAddress, poolApiAddress, poolAddress string, apiKeys []string) *ProxyServer {
	mux := http.NewServeMux()
	server := ProxyServer{
		httpServer: &http.Server{
			Addr:              proxyAddress,
			Handler:           mux,
			ReadHeaderTimeout: readHeaderTimeout,
			// ReadTimeout and WriteTimeout are not set: the response comes from
			// a tunnel, and an Ollama generation can take arbitrarily long.
			IdleTimeout: idleTimeout,
		},
	}
	server.tunnelService = tunnel.NewService(poolApiAddress, poolAddress, apiKeys)
	mux.HandleFunc("/", server.tunnelService.Handle)
	return &server
}

// MetricsHandler returns the Prometheus exposition handler. It is served by a
// separate MetricsServer on gateway.metrics.address, so the client proxy
// (proxy.address) is left to proxying requests only.
func (s *ProxyServer) MetricsHandler() http.Handler {
	return s.tunnelService.MetricsHandler()
}

// ListenAndServe starts the tunnel pool and the client proxy, blocking until
// both stop.
func (s *ProxyServer) ListenAndServe() error {
	go s.tunnelService.ListenAndServe()

	slog.Info("api server listening", "address", s.httpServer.Addr)
	if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Stop gracefully shuts down the client proxy and the tunnel pool.
func (s *ProxyServer) Stop(ctx context.Context) error {
	slog.Info("api server stopping")

	errs := make([]error, 0, 2)
	if err := s.httpServer.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("shutdown http server: %w", err))
	}
	// The tunnel pool listeners (pool.address / pool.apiAddress) also belong to
	// the gateway process, so they are closed here as well.
	if err := s.tunnelService.Stop(ctx); err != nil {
		errs = append(errs, fmt.Errorf("stop tunnel service: %w", err))
	}

	return errors.Join(errs...)
}
