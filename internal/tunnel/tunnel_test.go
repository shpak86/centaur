package tunnel

import (
	"errors"
	"io"
	"testing"
	"time"
)

// TestAliveTrueForIdleTunnel: a tunnel sitting in the registry is silent — that
// is its normal state, and the probe must consider such a connection alive.
// Otherwise the gateway would throw away every tunnel in a row.
func TestAliveTrueForIdleTunnel(t *testing.T) {
	conn, _ := newTCPConn(t)

	if !NewTunnel("id", conn, nil).alive() {
		t.Fatal("alive() = false for idle tunnel, want true")
	}
}

// TestAliveFalseForClosedTunnel: the connector closed the connection (FIN) — the
// tunnel is dead. We wait for EOF by reading: after it the probe always sees the
// closure, regardless of the race with FIN delivery.
func TestAliveFalseForClosedTunnel(t *testing.T) {
	conn, client := newTCPConn(t)
	if err := client.Close(); err != nil {
		t.Fatalf("close client conn: %v", err)
	}

	var probe [1]byte
	if _, err := conn.Read(probe[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("read tunnel after client close: error = %v, want EOF", err)
	}

	if NewTunnel("id", conn, nil).alive() {
		t.Fatal("alive() = true for closed tunnel, want false")
	}
}

// TestAliveFalseForResetTunnel: the connector was killed, the connection was
// reset (RST) — the read returns an error rather than EOF, and the tunnel is dead
// too.
func TestAliveFalseForResetTunnel(t *testing.T) {
	conn, client := newTCPConn(t)
	if err := client.SetLinger(0); err != nil {
		t.Fatalf("set linger: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close client conn: %v", err)
	}

	var probe [1]byte
	if _, err := conn.Read(probe[:]); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("read tunnel after reset: error = %v, want connection error", err)
	}

	if NewTunnel("id", conn, nil).alive() {
		t.Fatal("alive() = true for reset tunnel, want false")
	}
}

// TestAliveFalseForTunnelWithUnexpectedData: at rest a tunnel is silent, so any
// data in the stream means this is no longer the tunnel it claims to be.
func TestAliveFalseForTunnelWithUnexpectedData(t *testing.T) {
	conn, client := newTCPConn(t)
	tunnel := NewTunnel("id", conn, nil)

	if _, err := client.Write([]byte{'x'}); err != nil {
		t.Fatalf("write to tunnel: %v", err)
	}

	// The data may not arrive instantly, so we wait (no longer than a second).
	deadline := time.Now().Add(time.Second)
	for tunnel.alive() {
		if time.Now().After(deadline) {
			t.Fatal("alive() = true for tunnel with unexpected data, want false")
		}
		time.Sleep(time.Millisecond)
	}
}
