package tunnel

import (
	"net"
	"slices"
	"testing"
	"time"

	"github.com/shpak86/centaur/internal/metrics"
)

// newTCPConn отдаёт пару соединений на loopback: Registry.Bind принимает
// именно *net.TCPConn, поэтому фейковый net.Conn не подходит.
func newTCPConn(t *testing.T) (*net.TCPConn, net.Conn) {
	t.Helper()

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer listener.Close()

	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial tcp: %v", err)
	}

	server, err := listener.AcceptTCP()
	if err != nil {
		client.Close()
		t.Fatalf("accept tcp: %v", err)
	}

	t.Cleanup(func() { client.Close() })
	return server, client
}

// registerBound регистрирует туннель и биндит к нему соединение — так туннель
// выглядит в реестре после handshake коннектора.
func registerBound(t *testing.T, r *Registry, models ...string) {
	t.Helper()

	id := r.Register(models)
	conn, _ := newTCPConn(t)
	if err := r.Bind(id, conn); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
}

// TestAcquireRoutesByModel проверяет маршрутизацию по модели: выдаётся туннель,
// поддерживающий запрошенную модель, и он сразу исчезает из реестра.
func TestAcquireRoutesByModel(t *testing.T) {
	r := NewRegistry(metrics.New())
	registerBound(t, r, "llama3:8b")
	registerBound(t, r, "qwen2.5:7b")

	tunnel, ok := r.Acquire("qwen2.5:7b")
	if !ok {
		t.Fatal("Acquire(qwen2.5:7b) = false, want tunnel")
	}
	if !slices.Contains(tunnel.Models, "qwen2.5:7b") {
		t.Fatalf("Acquire() models = %v, want contain qwen2.5:7b", tunnel.Models)
	}

	// Туннель одноразовый: повторно он не выдаётся, других туннелей с этой
	// моделью нет.
	if _, ok := r.Acquire("qwen2.5:7b"); ok {
		t.Fatal("Acquire(qwen2.5:7b) = true, want false: tunnel must be removed")
	}

	// Туннель другой модели не тронут.
	tunnel, ok = r.Acquire("llama3:8b")
	if !ok {
		t.Fatal("Acquire(llama3:8b) = false, want tunnel")
	}
	if !slices.Contains(tunnel.Models, "llama3:8b") {
		t.Fatalf("Acquire() models = %v, want contain llama3:8b", tunnel.Models)
	}

	// Модели без коннектора нет ни в одном туннеле.
	if _, ok := r.Acquire("unknown"); ok {
		t.Fatal("Acquire(unknown) = true, want false")
	}
}

// TestAcquireEmptyRegistryReturnsImmediately: пустой реестр — мгновенный отказ.
// Порог заведомо меньше прежних 2 с ожидания: тест упадёт, если в Acquire
// вернут поллинг свободного туннеля.
func TestAcquireEmptyRegistryReturnsImmediately(t *testing.T) {
	r := NewRegistry(metrics.New())

	start := time.Now()
	tunnel, ok := r.Acquire("llama3:8b")
	elapsed := time.Since(start)

	if ok || tunnel != nil {
		t.Fatalf("Acquire() = (%v, %v), want (nil, false)", tunnel, ok)
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("Acquire() took %v, want immediate failure", elapsed)
	}
}

// TestAcquireSkipsUnboundTunnel: туннель, попавший в реестр до Bind (Conn == nil),
// не выдаётся — иначе RoundTrip писал бы в неготовое соединение.
func TestAcquireSkipsUnboundTunnel(t *testing.T) {
	r := NewRegistry(metrics.New())

	id := r.Register([]string{"llama3:8b"})

	if tunnel, ok := r.Acquire("llama3:8b"); ok {
		t.Fatalf("Acquire() = (%v, true), want (nil, false) before Bind", tunnel)
	}

	conn, _ := newTCPConn(t)
	if err := r.Bind(id, conn); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}

	if _, ok := r.Acquire("llama3:8b"); !ok {
		t.Fatal("Acquire() = false, want tunnel after Bind")
	}
	if _, ok := r.Acquire("llama3:8b"); ok {
		t.Fatal("Acquire() = true, want false: tunnel must be removed")
	}
}
