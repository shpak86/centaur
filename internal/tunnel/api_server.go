package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// httpServerOps holds the address and HTTP timeouts for ApiServer.
type httpServerOps struct {
	Address           string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// ApiServer serves the tunnel pool's HTTP API: health checks and tunnel
// registration.
type ApiServer struct {
	httpServer  *http.Server
	authManager *AuthManager
	registry    *Registry
}

// NewApiServer creates the tunnel pool HTTP API server protected by API-key
// authentication.
func NewApiServer(
	ops httpServerOps,
	authManager *AuthManager,
	registry *Registry,
) *ApiServer {
	server := ApiServer{
		authManager: authManager,
		httpServer: &http.Server{
			Addr:              ops.Address,
			ReadHeaderTimeout: ops.ReadHeaderTimeout,
			ReadTimeout:       ops.ReadTimeout,
			WriteTimeout:      ops.WriteTimeout,
			IdleTimeout:       ops.IdleTimeout,
		},
		registry: registry,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", server.handleHealthz)
	mux.HandleFunc("/api/v1/tunnels/register", server.handleRegisterTunnel)
	server.httpServer.Handler = AuthMiddleware(mux, authManager)
	return &server
}

// ListenAndServe starts the API server and blocks until it stops.
func (s *ApiServer) ListenAndServe() error {
	slog.Info("tunnel api server listening", "address", s.httpServer.Addr)
	if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Stop gracefully shuts down the API server.
func (s *ApiServer) Stop(ctx context.Context) error {
	slog.Info("tunnel api server stopping")
	return s.httpServer.Shutdown(ctx)
}

func (s *ApiServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("ok")); err != nil {
		slog.Debug("healthz: write response", "error", err)
	}
}

func (s *ApiServer) handleRegisterTunnel(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	data, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("Unprocessable tunnel registration", "error", err)
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}

	var req registerTunnelRequest
	if err := json.Unmarshal(data, &req); err != nil {
		slog.Error("Unprocessable tunnel registration", "error", err)
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}
	if len(req.Models) == 0 {
		slog.Error("Unprocessable tunnel registration", "error", "no models")
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}

	token := s.registry.Register(req.Models)
	resp := registerTunnelResponse{
		Token: token,
	}
	body, err := json.Marshal(resp)
	if err != nil {
		slog.Error("Marshal tunnel registration response", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(body); err != nil {
		slog.Debug("Write tunnel registration response", "error", err)
	}
}

// AuthMiddleware wraps next with an API-key check: a request without a valid
// ApiKey header gets 403.
func AuthMiddleware(next http.Handler, authManager *AuthManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get("ApiKey")
		if !authManager.IsValid(apiKey) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
