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

type ProxyServer struct {
	httpServer    *http.Server
	tunnelService *tunnel.Service
}

func NewProxyServer(proxyAddress, poolApiAddress, poolAddress string, apiKeys []string) *ProxyServer {
	mux := http.NewServeMux()
	server := ProxyServer{
		httpServer: &http.Server{
			Addr:              proxyAddress,
			Handler:           mux,
			ReadHeaderTimeout: readHeaderTimeout,
			// ReadTimeout и WriteTimeout не задавайте: ответ приходит из
			// туннеля, а генерация Ollama может длиться сколько угодно.
			IdleTimeout: idleTimeout,
		},
	}
	server.tunnelService = tunnel.NewService(poolApiAddress, poolAddress, apiKeys)
	mux.HandleFunc("/", server.tunnelService.Handle)
	return &server
}

// MetricsHandler отдаёт обработчик экспозиции Prometheus. Его обслуживает
// отдельный MetricsServer на gateway.metrics.address, поэтому клиентский
// прокси (proxy.address) остаётся только для проксирования запросов.
func (s *ProxyServer) MetricsHandler() http.Handler {
	return s.tunnelService.MetricsHandler()
}

func (s *ProxyServer) ListenAndServe() error {
	go s.tunnelService.ListenAndServe()

	slog.Info("api server listening", "address", s.httpServer.Addr)
	if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *ProxyServer) Stop(ctx context.Context) error {
	slog.Info("api server stopping")

	errs := make([]error, 0, 2)
	if err := s.httpServer.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("shutdown http server: %w", err))
	}
	// Слушатели пула туннелей (pool.address / pool.apiAddress) тоже
	// принадлежат процессу gateway, поэтому закрываем их здесь же.
	if err := s.tunnelService.Stop(ctx); err != nil {
		errs = append(errs, fmt.Errorf("stop tunnel service: %w", err))
	}

	return errors.Join(errs...)
}
