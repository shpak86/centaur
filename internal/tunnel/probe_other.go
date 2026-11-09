//go:build !unix

package tunnel

import "syscall"

// peekClosed cannot peek at the incoming stream without consuming it on
// platforms without MSG_PEEK (Windows), so it treats the connection as alive:
// the registry does not sweep dead tunnels there - as it was before the probe
// existed.
func peekClosed(_ syscall.RawConn) bool {
	return false
}
