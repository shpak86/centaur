//go:build unix

package tunnel

import (
	"errors"
	"syscall"
)

// peekClosed peeks at the socket's incoming stream without consuming data and
// answers "is the connection closed?": a FIN from the connector is 0 bytes, a
// reset is an error (RST and the like), and a live silent socket returns
// EAGAIN.
//
// Nothing is consumed (MSG_PEEK), so the probe is safe even when data is
// already present in the stream: the caller decides the connection's fate.
func peekClosed(raw syscall.RawConn) bool {
	closed := false

	err := raw.Control(func(fd uintptr) {
		var probe [1]byte
		n, _, err := syscall.Recvfrom(int(fd), probe[:], syscall.MSG_PEEK|syscall.MSG_DONTWAIT)
		switch {
		case errors.Is(err, syscall.EAGAIN), errors.Is(err, syscall.EWOULDBLOCK):
			// No data: the connection is alive and silent - just what we want.
			closed = false
		case err != nil:
			// Reset: RST, ENOTCONN and the like.
			closed = true
		case n > 0:
			// At rest a tunnel is silent, so data means this is no longer the
			// tunnel it claims to be.
			closed = true
		default:
			// n == 0: the connector closed the connection (FIN).
			closed = true
		}
	})
	if err != nil {
		// The connection is closed or closing (Control cannot get the file
		// descriptor), so there is nothing to check.
		return true
	}

	return closed
}
