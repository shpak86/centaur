// ollama-mock — тестовый сервер, который отвечает как Ollama API.
// Нужен, чтобы прогонять tunnel/connector/gateway без настоящей модели:
// поддерживает /api/generate, /api/chat (включая NDJSON-стрим), /api/tags,
// /api/ps и /api/version.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	readHeaderTimeout = 5 * time.Second
	idleTimeout       = 60 * time.Second
	// shutdownTimeout ограничивает время, отведённое серверу на graceful shutdown.
	shutdownTimeout = 10 * time.Second
	// promptLimit ограничивает длину промпта, попадающего в мок-ответ.
	promptLimit = 80
)

func main() {
	listen := flag.String("listen", ":11434", "Address to listen on")
	models := flag.String("models", "mock:latest,mock:small", "Comma-separated list of model names")
	chunks := flag.Int("chunks", 5, "Number of chunks in a streamed response")
	chunkDelay := flag.Duration("chunk-delay", 50*time.Millisecond, "Delay between streamed chunks")
	echoPrompt := flag.Bool("echo", true, "Include the request prompt in the mock response")
	logLevel := flag.String("log-level", "info", "Log level: debug, info, warn or error")
	flag.Parse()

	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		fmt.Fprintf(os.Stderr, "invalid -log-level %q: %v\n", *logLevel, err)
		os.Exit(2)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	modelList := make([]string, 0)
	for _, name := range strings.Split(*models, ",") {
		if name = strings.TrimSpace(name); name != "" {
			modelList = append(modelList, name)
		}
	}

	switch {
	case *listen == "":
		slog.Error("Invalid options", "error", "-listen is required")
		os.Exit(2)
	case len(modelList) == 0:
		slog.Error("Invalid options", "error", "-models is required")
		os.Exit(2)
	case *chunks < 1:
		slog.Error("Invalid options", "error", "-chunks must be positive")
		os.Exit(2)
	case *chunkDelay < 0:
		slog.Error("Invalid options", "error", "-chunk-delay must not be negative")
		os.Exit(2)
	}

	mock := &mock{
		models:     modelList,
		chunks:     *chunks,
		chunkDelay: *chunkDelay,
		echo:       *echoPrompt,
	}

	server := &http.Server{
		Addr:              *listen,
		Handler:           withLogging(mock.routes()),
		ReadHeaderTimeout: readHeaderTimeout,
		// ReadTimeout и WriteTimeout не задавайте: стрим идёт с паузами
		// между чанками, дедлайн оборвал бы его.
		IdleTimeout: idleTimeout,
	}

	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var serveErr error
	served := make(chan struct{})
	go func() {
		defer close(served)
		slog.Info("ollama mock listening", "address", server.Addr, "models", strings.Join(modelList, ","))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
			slog.Error("Ollama mock server", "error", err)
		}
		// сервер больше не работает — будим main, если он ждёт сигнал
		stop()
	}()

	<-shutdownCtx.Done()
	// возвращаем обработку сигналов по умолчанию:
	// повторный SIGINT/SIGTERM завершит процесс сразу
	stop()
	slog.Info("shutting down")

	stopCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(stopCtx); err != nil {
		slog.Error("Ollama mock shutdown", "error", err)
	}

	<-served
	slog.Info("stopped")

	if serveErr != nil {
		os.Exit(1)
	}
}

// mock отвечает в формате Ollama API, не обращаясь ни к какой модели.
type mock struct {
	models     []string
	chunks     int
	chunkDelay time.Duration
	echo       bool
}

func (m *mock) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", m.health)
	mux.HandleFunc("/api/version", m.version)
	mux.HandleFunc("/api/tags", m.tags)
	mux.HandleFunc("/api/ps", m.ps)
	mux.HandleFunc("/api/generate", m.generate)
	mux.HandleFunc("/api/chat", m.chat)
	return mux
}

// health отвечает как настоящая Ollama на GET /.
func (m *mock) health(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeError(w, http.StatusNotFound, "404 page not found")
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := w.Write([]byte("Ollama is running")); err != nil {
		slog.Debug("Write health response", "error", err)
	}
}

func (m *mock) version(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": "0.0.0-mock"})
}

func (m *mock) tags(w http.ResponseWriter, r *http.Request) {
	models := make([]tagModel, 0, len(m.models))
	for _, name := range m.models {
		models = append(models, tagModel{
			Name:       name,
			Model:      name,
			ModifiedAt: time.Now().UTC(),
			Size:       1024,
			Digest:     fmt.Sprintf("sha256:%064x", len(name)),
			Details: tagDetails{
				Format:            "gguf",
				Family:            "mock",
				ParameterSize:     "1B",
				QuantizationLevel: "Q4_0",
			},
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (m *mock) ps(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"models": []any{}})
}

type generateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	System string `json:"system"`
	Stream *bool  `json:"stream"`
}

// stream повторяет поведение Ollama: без поля stream ответ стримится.
func (r generateRequest) stream() bool {
	return r.Stream == nil || *r.Stream
}

type generateResponse struct {
	Model      string    `json:"model"`
	CreatedAt  time.Time `json:"created_at"`
	Response   string    `json:"response"`
	Done       bool      `json:"done"`
	DoneReason string    `json:"done_reason,omitempty"`
	EvalCount  int       `json:"eval_count,omitempty"`
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   *bool     `json:"stream"`
}

// stream повторяет поведение Ollama: без поля stream ответ стримится.
func (r chatRequest) stream() bool {
	return r.Stream == nil || *r.Stream
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Model      string    `json:"model"`
	CreatedAt  time.Time `json:"created_at"`
	Message    message   `json:"message"`
	Done       bool      `json:"done"`
	DoneReason string    `json:"done_reason,omitempty"`
	EvalCount  int       `json:"eval_count,omitempty"`
}

type tagModel struct {
	Name       string     `json:"name"`
	Model      string     `json:"model"`
	ModifiedAt time.Time  `json:"modified_at"`
	Size       int64      `json:"size"`
	Digest     string     `json:"digest"`
	Details    tagDetails `json:"details"`
}

type tagDetails struct {
	Format            string `json:"format"`
	Family            string `json:"family"`
	ParameterSize     string `json:"parameter_size"`
	QuantizationLevel string `json:"quantization_level"`
}

func (m *mock) generate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req generateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}

	model := m.pickModel(req.Model)
	chunks := m.responseChunks(req.Prompt)
	slog.Info("generate", "model", model, "promptLen", len(req.Prompt), "stream", req.stream())

	// Без stream Ollama отдаёт один JSON-документ целиком.
	if !req.stream() {
		writeJSON(w, http.StatusOK, generateResponse{
			Model:      model,
			CreatedAt:  time.Now().UTC(),
			Response:   strings.Join(chunks, ""),
			Done:       true,
			DoneReason: "stop",
			EvalCount:  len(chunks),
		})
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	for _, chunk := range chunks {
		resp := generateResponse{Model: model, CreatedAt: time.Now().UTC(), Response: chunk}
		if err := writeNDJSON(w, resp); err != nil {
			slog.Debug("Stream generate", "error", err)
			return
		}
		if !sleep(r.Context(), m.chunkDelay) {
			return
		}
	}

	resp := generateResponse{Model: model, CreatedAt: time.Now().UTC(), Done: true, DoneReason: "stop", EvalCount: len(chunks)}
	if err := writeNDJSON(w, resp); err != nil {
		slog.Debug("Stream generate", "error", err)
	}
}

func (m *mock) chat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}

	model := m.pickModel(req.Model)
	chunks := m.responseChunks(lastPrompt(req.Messages))
	slog.Info("chat", "model", model, "messages", len(req.Messages), "stream", req.stream())

	if !req.stream() {
		writeJSON(w, http.StatusOK, chatResponse{
			Model:      model,
			CreatedAt:  time.Now().UTC(),
			Message:    message{Role: "assistant", Content: strings.Join(chunks, "")},
			Done:       true,
			DoneReason: "stop",
			EvalCount:  len(chunks),
		})
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	for _, chunk := range chunks {
		resp := chatResponse{
			Model:     model,
			CreatedAt: time.Now().UTC(),
			Message:   message{Role: "assistant", Content: chunk},
		}
		if err := writeNDJSON(w, resp); err != nil {
			slog.Debug("Stream chat", "error", err)
			return
		}
		if !sleep(r.Context(), m.chunkDelay) {
			return
		}
	}

	resp := chatResponse{
		Model:      model,
		CreatedAt:  time.Now().UTC(),
		Message:    message{Role: "assistant", Content: ""},
		Done:       true,
		DoneReason: "stop",
		EvalCount:  len(chunks),
	}
	if err := writeNDJSON(w, resp); err != nil {
		slog.Debug("Stream chat", "error", err)
	}
}

// pickModel возвращает модель из запроса, а если её нет — первую из мока.
func (m *mock) pickModel(requested string) string {
	if requested != "" {
		return requested
	}
	return m.models[0]
}

// responseText строит текст мок-ответа, повторяя промпт клиента при -echo.
func (m *mock) responseText(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if !m.echo || prompt == "" {
		return "mock response from centaur ollama-mock"
	}

	if runes := []rune(prompt); len(runes) > promptLimit {
		prompt = string(runes[:promptLimit]) + "..."
	}

	return "mock response to: " + prompt
}

// responseChunks режет мок-ответ на m.chunks примерно равных частей:
// так стрим состоит из нескольких чанков и тайминги видно на глаз.
func (m *mock) responseChunks(prompt string) []string {
	runes := []rune(m.responseText(prompt))

	count := m.chunks
	if count > len(runes) {
		count = len(runes)
	}
	if count < 1 {
		return []string{string(runes)}
	}

	chunks := make([]string, 0, count)
	size := len(runes) / count
	extra := len(runes) % count
	start := 0
	for i := 0; i < count; i++ {
		end := start + size
		// первые extra чанков получают на один символ больше
		if i < extra {
			end++
		}
		chunks = append(chunks, string(runes[start:end]))
		start = end
	}

	return chunks
}

// lastPrompt берёт содержимое последнего непустого сообщения.
func lastPrompt(messages []message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if content := strings.TrimSpace(messages[i].Content); content != "" {
			return content
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("Write response", "error", err)
	}
}

// writeNDJSON отправляет v отдельной JSON-строкой и сразу отдаёт её клиенту.
func writeNDJSON(w http.ResponseWriter, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		return err
	}
	// ResponseController найдёт Flush и через обёртку statusRecorder.
	return http.NewResponseController(w).Flush()
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// sleep ждёт d или отмену запроса; false означает, что клиент отвалился.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// statusRecorder запоминает статус ответа для логов.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Unwrap отдаёт вложенный ResponseWriter: через него http.ResponseController
// находит Flush, поэтому стрим работает и сквозь логирование.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start).Round(time.Millisecond),
		)
	})
}
