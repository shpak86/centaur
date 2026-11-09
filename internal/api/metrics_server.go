package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

// MetricsServer serves Prometheus metrics on a dedicated address
// (gateway.metrics.address) so they can be administered independently of the
// client traffic on proxy.address: a separate port can be scraped, bound to
// loopback, closed at the perimeter, and so on.
type MetricsServer struct {
	httpServer *http.Server
}

// NewMetricsServer creates the metrics server. handler is the Prometheus
// exposition handler (usually the gateway metrics registry handler).
func NewMetricsServer(address string, handler http.Handler) *MetricsServer {
	return &MetricsServer{
		httpServer: &http.Server{
			Addr:              address,
			Handler:           handler,
			ReadHeaderTimeout: readHeaderTimeout,
			IdleTimeout:       idleTimeout,
		},
	}
}

// ListenAndServe starts the metrics server and blocks until it stops.
func (s *MetricsServer) ListenAndServe() error {
	slog.Info("metrics server listening", "address", s.httpServer.Addr)
	if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Stop gracefully shuts down the metrics server.
func (s *MetricsServer) Stop(ctx context.Context) error {
	slog.Info("metrics server stopping")
	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown metrics server: %w", err)
	}
	return nil
}
