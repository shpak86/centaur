package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

type tunnelServerOps struct {
	Address           string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}
type TunnelServer struct {
	ctx          context.Context
	cancel       context.CancelFunc
	ops          tunnelServerOps
	registry     *Registry
	tokenManager *TokenManager
	listener     *net.TCPListener
}

func (ts *TunnelServer) ListenAndServe() error {
	addr, err := net.ResolveTCPAddr("tcp", ts.ops.Address)
	if err != nil {
		slog.Error("Failed to resolve address", "error", err)
		return err
	}
	ts.listener, err = net.ListenTCP("tcp", addr)
	if err != nil {
		slog.Error("Failed to listen TCP", "error", err)
		return err
	}
	for {
		conn, err := ts.listener.AcceptTCP()
		if err != nil {
			// Stop() закрыл listener — это штатное завершение, а не ошибка.
			if errors.Is(err, net.ErrClosed) {
				slog.Info("tunnel server stopped")
				return nil
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			slog.Error("Accept tunnel", "error", err)
			continue
		}
		// readHandshake читает токен с дедлайном, поэтому соединения
		// обрабатываем параллельно: иначе один медленный коннектор
		// блокирует приём остальных туннелей.
		go ts.handleTunnel(conn)
	}
}

func readHandshake(conn *net.TCPConn) ([]byte, error) {
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetReadDeadline(time.Time{})
	token := make([]byte, 16)
	_, err := io.ReadFull(conn, token)
	if err != nil {
		return nil, fmt.Errorf("read handshake: %w", err)
	}
	return token, nil
}

func (ts *TunnelServer) handleTunnel(conn *net.TCPConn) error {
	conn.SetNoDelay(true)
	conn.SetKeepAlive(true)
	conn.SetKeepAlivePeriod(30 * time.Second)

	// Соединение передаётся в пул только при успешном handshake.
	// На успешном пути его закрывает тот, кто заберёт туннель из пула,
	// поэтому закрываем conn только при выходе по ошибке.
	registered := false
	defer func() {
		if registered {
			return
		}
		if err := conn.Close(); err != nil {
			slog.Error("Close tunnel connection", "error", err)
		}
	}()

	token, err := readHandshake(conn)
	if err != nil {
		slog.Error("Tunnel handshake", "error", err)
		return err
	}
	if !ts.tokenManager.Acquire(string(token)) {
		err := fmt.Errorf("unknown token %s", token)
		slog.Error("Invalid token", "error", err)
		return err
	}

	ts.registry.Add(NewTunnel(string(token), conn))
	registered = true
	slog.Info("tunnel registered")

	return nil
}

func (ts *TunnelServer) Stop() error {
	var err error
	// listener == nil, если ListenAndServe не смог занять адрес.
	if ts.listener != nil {
		if cerr := ts.listener.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) {
			err = cerr
		}
	}

	wg := sync.WaitGroup{}
	for _, it := range ts.registry.Tunnels() {
		wg.Go(func() {
			if err := it.Conn.Close(); err != nil {
				slog.Debug("Close tunnel connection", "error", err)
			}
		})
	}
	wg.Wait()

	return err
}

func NewTunnelServer(
	ops tunnelServerOps,
	tokenManager *TokenManager,
	registry *Registry,
) *TunnelServer {
	server := TunnelServer{
		ops:          ops,
		registry:     registry,
		tokenManager: tokenManager,
	}

	return &server
}
