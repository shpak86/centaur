package tunnel

import (
	"net"
	"time"
)

// Tunnel is a single registered tunnel: an outbound connection from a connector
// that serves exactly one client request before it is closed.
type Tunnel struct {
	Id           string
	Conn         *net.TCPConn
	Models       []string
	RegisteredAt time.Time
}

// NewTunnel creates a tunnel registered at the current time. conn may be nil
// until the connector completes the handshake (see Registry.Bind).
func NewTunnel(id string, conn *net.TCPConn, models []string) *Tunnel {
	t := Tunnel{
		Id:           id,
		Conn:         conn,
		Models:       models,
		RegisteredAt: time.Now(),
	}
	return &t
}

// Close closes the tunnel connection.
func (t *Tunnel) Close() {
	t.Conn.Close()
}

// alive reports whether the tunnel connection is still alive.
//
// The probe peeks at the socket's incoming stream without consuming it
// (MSG_PEEK): a connector close shows up as 0 bytes (FIN), a reset as an error
// (RST and the like), and a healthy silent socket as EAGAIN.
//
// Core invariant: a tunnel sitting in the registry is guaranteed to be silent -
// only RoundTrip writes to it, and only after Acquire, while the connector only
// writes a response to a request. So any data present at rest means the tunnel
// is no longer what it claims to be and must not be handed to a client.
//
// A read deadline in the past (pastDeadline) does not work for the probe:
// internal/poll checks the deadline before the syscall, so a dead socket
// answers exactly like a live one.
//
// A break without FIN/RST (a "black hole") is invisible to the probe; it is
// caught by the TCP keepalive enabled by the tunnel server (see
// TunnelServer.handleTunnel).
func (t *Tunnel) alive() bool {
	raw, err := t.Conn.SyscallConn()
	if err != nil {
		return false
	}
	return !peekClosed(raw)
}
