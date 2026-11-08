package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

// MetricsServer отдаёт метрики Prometheus на отдельном адресе
// (gateway.metrics.address), чтобы администрировать их независимо от
// клиентского трафика на proxy.address: свой порт можно скрейпить, слушать на
// loopback, закрывать на периметре и т.д.
type MetricsServer struct {
	httpServer *http.Server
}

// NewMetricsServer создаёт сервер метрик. handler — обработчик экспозиции
// Prometheus (обычно handler реестра метрик gateway).
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

func (s *MetricsServer) ListenAndServe() error {
	slog.Info("metrics server listening", "address", s.httpServer.Addr)
	if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *MetricsServer) Stop(ctx context.Context) error {
	slog.Info("metrics server stopping")
	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown metrics server: %w", err)
	}
	return nil
}
