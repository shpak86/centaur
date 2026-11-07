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
	ctx      context.Context
	cancel   context.CancelFunc
	ops      tunnelServerOps
	registry *Registry
	listener *net.TCPListener
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

func readHandshake(conn *net.TCPConn) (string, error) {
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetReadDeadline(time.Time{})
	token := make([]byte, 16)
	_, err := io.ReadFull(conn, token)
	if err != nil {
		return "", fmt.Errorf("read handshake: %w", err)
	}
	return string(token), nil
}

func (ts *TunnelServer) handleTunnel(conn *net.TCPConn) error {
	conn.SetNoDelay(true)
	conn.SetKeepAlive(true)
	conn.SetKeepAlivePeriod(30 * time.Second)

	token, err := readHandshake(conn)
	if err != nil {
		slog.Error("Tunnel handshake", "error", err)
		return err
	}

	err = ts.registry.Bind(token, conn)
	if err != nil {
		slog.Error("Unable to bind conn to the tunnel", "token", token, "remote", conn.RemoteAddr().String())
		return err
	}
	// registered = true
	slog.Debug("Tunnel bind", "token", token, "remote", conn.RemoteAddr().String())

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
	for it := range ts.registry.Tunnels() {
		if it.Conn == nil {
			// Туннель зарегистрирован, но коннектор ещё не привязал соединение.
			continue
		}
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
	registry *Registry,
) *TunnelServer {
	server := TunnelServer{
		ops:      ops,
		registry: registry,
	}

	return &server
}
