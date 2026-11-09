package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shpak86/centaur/internal/api"
)

// shutdownTimeout ограничивает время, отведённое серверам на graceful shutdown.
const shutdownTimeout = 10 * time.Second

// ServerConfiguration описывает содержимое файла конфигурации,
// который передаётся в -config (по умолчанию /etc/centaur/gateway.json).
type ServerConfiguration struct {
	Logger  LoggerConfiguration  `json:"logger"`
	Proxy   ProxyConfiguration   `json:"proxy"`
	Metrics MetricsConfiguration `json:"metrics"`
	Pool    PoolConfiguration    `json:"pool"`
}

type LoggerConfiguration struct {
	// Level — уровень логирования: debug, info, warn или error.
	Level string `json:"level"`
}

type ProxyConfiguration struct {
	// Address — адрес, на котором gateway принимает клиентские запросы.
	Address string `json:"address"`
}

type MetricsConfiguration struct {
	// Address — listen-адрес HTTP-сервера метрик Prometheus (host:port, без
	// схемы). Отдельный от proxy.address, чтобы метрики не пересекались с
	// клиентским трафиком.
	Address string `json:"address"`
}

type PoolConfiguration struct {
	// ApiAddress — listen-адрес HTTP API пула туннелей (host:port, без схемы):
	// на нём gateway принимает регистрации туннелей от коннекторов.
	ApiAddress string `json:"apiAddress"`
	// Address — адрес, на котором пул туннелей принимает соединения.
	Address string   `json:"address"`
	ApiKeys []string `json:"keys"`
}

func loadConfig(path string) (ServerConfiguration, error) {
	var config ServerConfiguration

	data, err := os.ReadFile(path)
	if err != nil {
		return config, fmt.Errorf("read config %q: %w", path, err)
	}

	if err := json.Unmarshal(data, &config); err != nil {
		return config, fmt.Errorf("parse config %q: %w", path, err)
	}

	if err := validateConfig(config); err != nil {
		return config, fmt.Errorf("validate config %q: %w", path, err)
	}

	return config, nil
}

func validateConfig(config ServerConfiguration) error {
	switch {
	case config.Proxy.Address == "":
		return fmt.Errorf("proxy.address is required")
	// listen-адрес метрик, а не URL: сервер со схемой в адресе просто не поднялся бы.
	case config.Metrics.Address == "":
		return fmt.Errorf("metrics.address is required")
	case strings.Contains(config.Metrics.Address, "://"):
		return fmt.Errorf("metrics.address must be a listen address like 127.0.0.1:18183, got URL %q", config.Metrics.Address)
	case config.Pool.Address == "":
		return fmt.Errorf("pool.address is required")
	case config.Pool.ApiAddress == "":
		return fmt.Errorf("pool.apiAddress is required")
	// В отличие от коннектора, здесь apiAddress — listen-адрес, а не URL:
	// сервер со схемой в адресе просто не поднялся бы.
	case strings.Contains(config.Pool.ApiAddress, "://"):
		return fmt.Errorf("pool.apiAddress must be a listen address like 127.0.0.1:8082, got URL %q", config.Pool.ApiAddress)
	}
	return nil
}

func prepareLogger(config LoggerConfiguration) error {
	level := slog.LevelInfo
	if config.Level != "" {
		if err := level.UnmarshalText([]byte(config.Level)); err != nil {
			return fmt.Errorf("parse logger level %q: %w", config.Level, err)
		}
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))

	return nil
}

func main() {
	configPath := flag.String("config", "/etc/centaur/gateway.json", "Configuration path")
	flag.Parse()

	config, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("Load configuration", "error", err)
		os.Exit(1)
	}

	if err := prepareLogger(config.Logger); err != nil {
		slog.Error("Prepare logger", "error", err)
		os.Exit(1)
	}

	slog.Info("configuration loaded", "path", *configPath)

	// shutdownCtx отменяется по SIGINT/SIGTERM (или когда сервер упал сам).
	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	proxyServer := api.NewProxyServer(
		config.Proxy.Address,
		config.Pool.ApiAddress,
		config.Pool.Address,
		config.Pool.ApiKeys,
	)
	// Метрики — на отдельном сервере и порту (metrics.address), чтобы не
	// смешиваться с клиентским трафиком proxy.address.
	metricsServer := api.NewMetricsServer(config.Metrics.Address, proxyServer.MetricsHandler())

	var (
		proxyErr   error
		metricsErr error
	)
	wg := sync.WaitGroup{}
	wg.Go(func() {
		proxyErr = proxyServer.ListenAndServe()
		if proxyErr != nil {
			slog.Error("Proxy server", "error", proxyErr)
		}
		// сервер больше не работает — будим main, если он ждёт сигнал
		stop()
	})
	wg.Go(func() {
		metricsErr = metricsServer.ListenAndServe()
		if metricsErr != nil {
			slog.Error("Metrics server", "error", metricsErr)
		}
		// сервер больше не работает — будим main, если он ждёт сигнал
		stop()
	})

	<-shutdownCtx.Done()
	// возвращаем обработку сигналов по умолчанию:
	// повторный SIGINT/SIGTERM завершит процесс сразу
	stop()
	slog.Info("shutting down")

	stopCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := proxyServer.Stop(stopCtx); err != nil {
		slog.Error("Proxy server shutdown", "error", err)
	}
	if err := metricsServer.Stop(stopCtx); err != nil {
		slog.Error("Metrics server shutdown", "error", err)
	}

	wg.Wait()
	slog.Info("stopped")

	if proxyErr != nil || metricsErr != nil {
		os.Exit(1)
	}
}
