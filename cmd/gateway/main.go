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

// shutdownTimeout limits the time given to the servers for a graceful shutdown.
const shutdownTimeout = 10 * time.Second

// ServerConfiguration describes the contents of the configuration file passed
// to -config (default /etc/centaur/gateway.json).
type ServerConfiguration struct {
	Logger  LoggerConfiguration  `json:"logger"`
	Proxy   ProxyConfiguration   `json:"proxy"`
	Metrics MetricsConfiguration `json:"metrics"`
	Pool    PoolConfiguration    `json:"pool"`
}

// LoggerConfiguration describes the logger settings.
type LoggerConfiguration struct {
	// Level is the log level: debug, info, warn or error.
	Level string `json:"level"`
}

// ProxyConfiguration describes the client-facing listener.
type ProxyConfiguration struct {
	// Address is the address where the gateway accepts client requests.
	Address string `json:"address"`
}

// MetricsConfiguration describes the Prometheus metrics listener.
type MetricsConfiguration struct {
	// Address is the listen address of the Prometheus metrics HTTP server
	// (host:port, no scheme). It is separate from proxy.address so metrics do
	// not share the client traffic.
	Address string `json:"address"`
}

// PoolConfiguration describes the tunnel pool listeners.
type PoolConfiguration struct {
	// ApiAddress is the listen address of the tunnel pool HTTP API (host:port,
	// no scheme): the gateway accepts tunnel registrations from connectors here.
	ApiAddress string `json:"apiAddress"`
	// Address is the address where the tunnel pool accepts connections.
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
	// This is the metrics listen address, not a URL: a server with a scheme in
	// its address simply would not start.
	case config.Metrics.Address == "":
		return fmt.Errorf("metrics.address is required")
	case strings.Contains(config.Metrics.Address, "://"):
		return fmt.Errorf("metrics.address must be a listen address like 127.0.0.1:18183, got URL %q", config.Metrics.Address)
	case config.Pool.Address == "":
		return fmt.Errorf("pool.address is required")
	case config.Pool.ApiAddress == "":
		return fmt.Errorf("pool.apiAddress is required")
	// Unlike in the connector, apiAddress here is a listen address, not a URL:
	// a server with a scheme in its address simply would not start.
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

	// shutdownCtx is cancelled by SIGINT/SIGTERM (or when a server dies on its own).
	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	proxyServer := api.NewProxyServer(
		config.Proxy.Address,
		config.Pool.ApiAddress,
		config.Pool.Address,
		config.Pool.ApiKeys,
	)
	// Metrics live on a separate server and port (metrics.address) so they do
	// not mix with the client traffic on proxy.address.
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
		// the server is no longer running — wake main if it is waiting for a signal
		stop()
	})
	wg.Go(func() {
		metricsErr = metricsServer.ListenAndServe()
		if metricsErr != nil {
			slog.Error("Metrics server", "error", metricsErr)
		}
		// the server is no longer running — wake main if it is waiting for a signal
		stop()
	})

	<-shutdownCtx.Done()
	// restore the default signal handling:
	// a second SIGINT/SIGTERM will terminate the process immediately
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
