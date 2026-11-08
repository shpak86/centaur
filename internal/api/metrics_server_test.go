package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shpak86/centaur/internal/tunnel"
)

// metricsTestTimeout ограничивает ожидание готовности сервера метрик.
const metricsTestTimeout = 5 * time.Second

// freeListenAddr занимает свободный TCP-порт на loopback и сразу освобождает
// его, чтобы передать адрес серверу метрик.
func freeListenAddr(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen loopback: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close loopback listener: %v", err)
	}
	return addr
}

// TestMetricsServerServesOnItsOwnPort проверяет, что сервер метрик поднимается
// на собственном адресе (отдельно от proxy.address) и отдаёт экспозицию
// Prometheus.
func TestMetricsServerServesOnItsOwnPort(t *testing.T) {
	service := tunnel.NewService("127.0.0.1:0", "127.0.0.1:0", nil)
	server := NewMetricsServer(freeListenAddr(t), service.MetricsHandler())

	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = server.ListenAndServe()
	}()
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), metricsTestTimeout)
		defer cancel()
		if err := server.Stop(stopCtx); err != nil {
			t.Errorf("Stop() error = %v", err)
		}
		select {
		case <-served:
		case <-time.After(metricsTestTimeout):
			t.Error("ListenAndServe() did not stop after Stop()")
		}
	})

	body := scrapeMetrics(t, server.httpServer.Addr)
	for _, want := range []string{
		"centaur_gateway_tunnels",
		"centaur_gateway_tunnel_registrations_total",
		"go_goroutines",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics body does not contain %q", want)
		}
	}
}

// scrapeMetrics ждёт готовности сервера метрик и возвращает тело /metrics.
func scrapeMetrics(t *testing.T, addr string) string {
	t.Helper()

	deadline := time.Now().Add(metricsTestTimeout)
	for {
		resp, err := http.Get("http://" + addr + "/metrics")
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("/metrics status = %d, want %d", resp.StatusCode, http.StatusOK)
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read /metrics body: %v", err)
			}
			return string(body)
		}
		if time.Now().After(deadline) {
			t.Fatalf("metrics server did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
