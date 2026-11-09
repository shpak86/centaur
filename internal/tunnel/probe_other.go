//go:build !unix

package tunnel

import "syscall"

// peekClosed на платформах без MSG_PEEK (Windows) посмотреть во входящий поток,
// ничего не потребляя, не умеет, поэтому считает соединение живым: реестр там
// мёртвые туннели не вычищает — как было до появления пробы.
func peekClosed(_ syscall.RawConn) bool {
	return false
}
