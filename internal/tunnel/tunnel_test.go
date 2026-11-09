package tunnel

import (
	"errors"
	"io"
	"testing"
	"time"
)

// TestAliveTrueForIdleTunnel: туннель, лежащий в реестре, молчит — это его
// нормальное состояние, и проба обязана считать такое соединение живым. Иначе
// gateway выбрасывал бы все туннели подряд.
func TestAliveTrueForIdleTunnel(t *testing.T) {
	conn, _ := newTCPConn(t)

	if !NewTunnel("id", conn, nil).alive() {
		t.Fatal("alive() = false for idle tunnel, want true")
	}
}

// TestAliveFalseForClosedTunnel: коннектор закрыл соединение (FIN) — туннель
// мёртв. EOF ждём чтением: после него проба видит закрытие всегда, независимо
// от гонки с доставкой FIN.
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

// TestAliveFalseForResetTunnel: коннектор убит, соединение оборвано (RST) —
// чтение отдаёт ошибку, а не EOF, и туннель тоже мёртв.
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

// TestAliveFalseForTunnelWithUnexpectedData: в покое туннель молчит, поэтому
// любые данные в потоке означают, что это уже не тот туннель, за которого он
// себя выдаёт.
func TestAliveFalseForTunnelWithUnexpectedData(t *testing.T) {
	conn, client := newTCPConn(t)
	tunnel := NewTunnel("id", conn, nil)

	if _, err := client.Write([]byte{'x'}); err != nil {
		t.Fatalf("write to tunnel: %v", err)
	}

	// Данные могут дойти не мгновенно, поэтому ждём (не дольше секунды).
	deadline := time.Now().Add(time.Second)
	for tunnel.alive() {
		if time.Now().After(deadline) {
			t.Fatal("alive() = true for tunnel with unexpected data, want false")
		}
		time.Sleep(time.Millisecond)
	}
}
