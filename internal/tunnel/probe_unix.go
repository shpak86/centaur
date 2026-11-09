//go:build unix

package tunnel

import (
	"errors"
	"syscall"
)

// peekClosed смотрит во входящий поток сокета, не потребляя данные, и отвечает
// на вопрос «соединение закрыто?»: FIN от коннектора — это 0 байт, обрыв — это
// ошибка (RST и подобное), а живой молчащий сокет отвечает EAGAIN.
//
// Ничего не потребляется (MSG_PEEK), поэтому проба безопасна в том числе если
// в потоке что-то есть: решение о судьбе соединения принимает вызывающий.
func peekClosed(raw syscall.RawConn) bool {
	closed := false

	err := raw.Control(func(fd uintptr) {
		var probe [1]byte
		n, _, err := syscall.Recvfrom(int(fd), probe[:], syscall.MSG_PEEK|syscall.MSG_DONTWAIT)
		switch {
		case errors.Is(err, syscall.EAGAIN), errors.Is(err, syscall.EWOULDBLOCK):
			// Данных нет: соединение живо и молчит — то, что нужно.
			closed = false
		case err != nil:
			// Обрыв: RST, ENOTCONN и прочее.
			closed = true
		case n > 0:
			// В покое туннель молчит, поэтому данные означают, что это уже не
			// тот туннель, за которого он себя выдаёт.
			closed = true
		default:
			// n == 0 — коннектор закрыл соединение (FIN).
			closed = true
		}
	})
	if err != nil {
		// Соединение закрыто или закрывается (Control не даёт дескриптор),
		// проверять нечего.
		return true
	}

	return closed
}
