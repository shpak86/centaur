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

// shutdownTimeout ограничивает время, отведённое клиенту на graceful shutdown.
const shutdownTimeout = 10 * time.Second

// ClientConfiguration описывает содержимое файла конфигурации,
// который передаётся в -config (по умолчанию /etc/centaur/connector.json).
type ClientConfiguration struct {
	Logger LoggerConfiguration `json:"logger"`
	Pool   PoolConfiguration   `json:"pool"`
	Target TargetConfiguration `json:"target"`
}

type LoggerConfiguration struct {
	// Level — уровень логирования: debug, info, warn или error.
	Level string `json:"level"`
}

type PoolConfiguration struct {
	// ApiAddress — адрес HTTP API пула туннелей.
	ApiAddress string `json:"apiAddress"`
	// Address — адрес, на котором пул туннелей принимает соединения.
	Address string `json:"address"`
	ApiKey  string `json:"key"`
}

type TargetConfiguration struct {
	// Address — URL локального сервиса (например, Ollama),
	// куда коннектор проксирует запросы из туннеля.
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
	// gateway ждёт здесь listen-адрес, а коннектору нужен URL со схемой:
	// без неё url.JoinPath в клиенте падает и коннектор молча ретраится.
	case !isURL(config.Pool.ApiAddress):
		return fmt.Errorf("pool.apiAddress must be an URL with scheme like http://127.0.0.1:8082, got %q", config.Pool.ApiAddress)
	case config.Target.Address == "":
		return fmt.Errorf("target.address is required")
	case !isURL(config.Target.Address):
		return fmt.Errorf("target.address must be an URL with scheme like http://127.0.0.1:11434, got %q", config.Target.Address)
	}
	return nil
}

// isURL проверяет, что адрес — абсолютный URL со схемой и хостом.
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

	// -config — путь к файлу конфигурации коннектора.
	configPath := flag.String("config", "/etc/centaur/connector.json", "Configuration path")
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

	// shutdownCtx отменяется по SIGINT/SIGTERM (или когда клиент завершился сам).
	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client := tunnel.NewClient(
		config.Pool.ApiAddress,
		config.Pool.Address,
		config.Target.Address,
		config.Pool.ApiKey,
		config.Target.Models,
	)
	slog.Info("connector started",
		"poolApiAddress", config.Pool.ApiAddress,
		"poolAddress", config.Pool.Address,
		"targetAddress", config.Target.Address,
	)

	// Serve блокируется, пока работает текущий туннель, поэтому держим его
	// в отдельной горутине и ждём сигнала в main.
	served := make(chan struct{})
	go func() {
		defer close(served)
		client.Serve()
		// клиент больше не работает — будим main, если он ждёт сигнал
		stop()
	}()

	<-shutdownCtx.Done()
	// возвращаем обработку сигналов по умолчанию:
	// повторный SIGINT/SIGTERM завершит процесс сразу
	stop()
	slog.Info("shutting down")

	// Close отменяет контекст клиента: новые туннели уже не поднимаются,
	// текущему даём shutdownTimeout на завершение (in-flight запросы).
	client.Close()

	select {
	case <-served:
		slog.Info("stopped")
	case <-time.After(shutdownTimeout):
		slog.Warn("connector did not stop in time", "timeout", shutdownTimeout)
	}

}
