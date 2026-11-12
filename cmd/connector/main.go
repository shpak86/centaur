package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shpak86/centaur/internal/tunnel"
)

// shutdownTimeout limits the time given to the client for a graceful shutdown.
const shutdownTimeout = 10 * time.Second

// ClientConfiguration describes the contents of the configuration file passed
// to -config (default /etc/centaur/connector.json).
type ClientConfiguration struct {
	Logger LoggerConfiguration `json:"logger"`
	Pool   PoolConfiguration   `json:"pool"`
	Target TargetConfiguration `json:"target"`
}

// LoggerConfiguration describes the logger settings.
type LoggerConfiguration struct {
	// Level is the log level: debug, info, warn or error.
	Level string `json:"level"`
}

// PoolConfiguration describes the tunnel pool the connector connects to.
type PoolConfiguration struct {
	// ApiAddress is the address of the tunnel pool HTTP API.
	ApiAddress string `json:"apiAddress"`
	// Address is the address where the tunnel pool accepts connections.
	Address string `json:"address"`
	ApiKey  string `json:"key"`
	Tunnels int    `json:"tunnels"`
}

// TargetConfiguration describes the local service requests are proxied to.
type TargetConfiguration struct {
	// Address is the URL of the local service (for example, Ollama) that the
	// connector proxies tunnel requests to.
	Address string   `json:"address"`
	Models  []string `json:"models"`
}

func loadConfig(path string) (ClientConfiguration, error) {
	var config ClientConfiguration

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

func validateConfig(config ClientConfiguration) error {
	switch {
	case config.Pool.Address == "":
		return fmt.Errorf("pool.address is required")
	case config.Pool.ApiAddress == "":
		return fmt.Errorf("pool.apiAddress is required")
	// The gateway expects a listen address here, while the connector needs a URL
	// with a scheme: without it url.JoinPath in the client fails and the
	// connector silently retries.
	case !isURL(config.Pool.ApiAddress):
		return fmt.Errorf("pool.apiAddress must be an URL with scheme like http://127.0.0.1:8082, got %q", config.Pool.ApiAddress)
	case config.Target.Address == "":
		return fmt.Errorf("target.address is required")
	case !isURL(config.Target.Address):
		return fmt.Errorf("target.address must be an URL with scheme like http://127.0.0.1:11434, got %q", config.Target.Address)
	case config.Pool.Tunnels <= 0:
		return fmt.Errorf("pool.tunnels must be positive, got %d", config.Pool.Tunnels)
	}
	return nil
}

// isURL reports whether address is an absolute URL with a scheme and host.
func isURL(address string) bool {
	u, err := url.Parse(address)
	return err == nil && u.Scheme != "" && u.Host != ""
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

	// -config is the path to the connector configuration file.
	configPath := flag.String("config", "~/.centaur/config/connector.json", "Configuration path")
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

	// shutdownCtx is cancelled by SIGINT/SIGTERM (or when the client finishes on its own).
	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client := tunnel.NewClient(
		config.Pool.ApiAddress,
		config.Pool.Address,
		config.Target.Address,
		config.Pool.ApiKey,
		config.Target.Models,
		config.Pool.Tunnels,
	)
	slog.Info("connector started",
		"poolApiAddress", config.Pool.ApiAddress,
		"poolAddress", config.Pool.Address,
		"targetAddress", config.Target.Address,
	)

	// Serve blocks while the current tunnel is running, so it runs in a separate
	// goroutine while main waits for a signal.
	served := make(chan struct{})
	go func() {
		defer close(served)
		client.Serve()
		// the client is no longer running — wake main if it is waiting for a signal
		stop()
	}()

	<-shutdownCtx.Done()
	// restore the default signal handling:
	// a second SIGINT/SIGTERM will terminate the process immediately
	stop()
	slog.Info("shutting down")

	// Close cancels the client's context: new tunnels are no longer brought up,
	// and the current one is given shutdownTimeout to finish (in-flight requests).
	client.Close()

	select {
	case <-served:
		slog.Info("stopped")
	case <-time.After(shutdownTimeout):
		slog.Warn("connector did not stop in time", "timeout", shutdownTimeout)
	}

}
