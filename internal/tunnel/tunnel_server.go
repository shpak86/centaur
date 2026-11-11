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

// tunnelServerOps holds the address and HTTP timeouts for TunnelServer.
type tunnelServerOps struct {
	Address           string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// TunnelServer accepts connector tunnel connections and binds them to registered
// tunnels using the handshake token.
type TunnelServer struct {
	ctx      context.Context
	cancel   context.CancelFunc
	ops      tunnelServerOps
	registry *Registry
	listener *net.TCPListener
}

// ListenAndServe accepts tunnel connections until Stop closes the listener.
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
			// Stop closed the listener — a normal shutdown, not an error.
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
		// readHandshake reads the token under a deadline, so connections are
		// handled concurrently: otherwise one slow connector would block the
		// acceptance of other tunnels.
		go ts.handleTunnel(conn)
	}
}

// readHandshake reads the 16-byte handshake token under a read deadline.
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

// handleTunnel reads the handshake and binds the connection to the matching
// tunnel. It enables TCP keepalive so a silently broken connection is eventually
// detected.
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
	slog.Debug("Tunnel bind", "token", token, "remote", conn.RemoteAddr().String())
	return nil
}

// Stop closes the listener and all bound tunnel connections.
func (ts *TunnelServer) Stop() error {
	var err error
	// listener is nil if ListenAndServe could not take the address.
	if ts.listener != nil {
		if cerr := ts.listener.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) {
			err = cerr
		}
	}

	wg := sync.WaitGroup{}
	for it := range ts.registry.Tunnels() {
		if it.Conn == nil {
			// The tunnel is registered but the connector has not bound a
			// connection yet.
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

// NewTunnelServer creates a tunnel server that binds connections to registry
// tunnels.
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
