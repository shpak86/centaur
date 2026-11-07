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

type httpServerOps struct {
	Address           string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

type ApiServer struct {
	httpServer   *http.Server
	tokenManager *TokenManager
	authManager  *AuthManager
}

func NewApiServer(
	ops httpServerOps,
	tokenManager *TokenManager,
	authManager *AuthManager,
) *ApiServer {
	server := ApiServer{
		authManager:  authManager,
		tokenManager: tokenManager,
		httpServer: &http.Server{
			Addr:              ops.Address,
			ReadHeaderTimeout: ops.ReadHeaderTimeout,
			ReadTimeout:       ops.ReadTimeout,
			WriteTimeout:      ops.WriteTimeout,
			IdleTimeout:       ops.IdleTimeout,
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", server.handleHealthz)
	mux.HandleFunc("/api/v1/tunnels/register", server.handleRegisterTunnel)
	server.httpServer.Handler = AuthMiddleware(mux, authManager)
	return &server
}

func (s *ApiServer) ListenAndServe() error {
	slog.Info("tunnel api server listening", "address", s.httpServer.Addr)
	if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

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

	resp := registerTunnelResponse{
		Token: s.tokenManager.Get(),
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
