package tunnel

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shpak86/centaur/internal/metrics"
)

// newTCPConn отдаёт пару соединений на loopback: Registry.Bind принимает
// именно *net.TCPConn, поэтому фейковый net.Conn не подходит.
func newTCPConn(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer listener.Close()

	client, err := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr))
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

// registerDeadBound регистрирует туннель, коннектор которого уже упал:
// клиентскую половину соединения закрываем и дожидаемся EOF на серверной.
// После EOF проба живости всегда видит мёртвое соединение — гонки с доставкой
// FIN нет.
func registerDeadBound(t *testing.T, r *Registry, models ...string) {
	t.Helper()

	id := r.Register(models)
	conn, client := newTCPConn(t)
	if err := r.Bind(id, conn); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close client conn: %v", err)
	}

	var probe [1]byte
	if _, err := conn.Read(probe[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("read tunnel after client close: error = %v, want EOF", err)
	}
}

// expire сдвигает регистрацию в прошлое: так pendingTTL проверяется без
// ожидания.
func expire(t *testing.T, r *Registry, id string) {
	t.Helper()

	r.mu.Lock()
	defer r.mu.Unlock()

	tunnel, exists := r.tunnels[id]
	if !exists {
		t.Fatalf("tunnel %q not found in registry", id)
	}
	tunnel.RegisteredAt = time.Now().Add(-2 * pendingTTL)
}

// waitEmptyRegistry ждёт, пока реестр опустеет (не дольше timeout).
func waitEmptyRegistry(t *testing.T, r *Registry, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		pending, ready := r.counts()
		if pending == 0 && ready == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("registry is not empty: pending = %d, ready = %d", pending, ready)
		}
		time.Sleep(time.Millisecond)
	}
}

// scrape отдаёт метрики в текстовом формате Prometheus.
func scrape(t *testing.T, m *metrics.Metrics) string {
	t.Helper()

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want %d", rec.Code, http.StatusOK)
	}
	return rec.Body.String()
}

// assertMetrics проверяет, что выдача метрик содержит все ожидаемые строки.
func assertMetrics(t *testing.T, m *metrics.Metrics, want ...string) {
	t.Helper()

	body := scrape(t, m)
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("metrics output does not contain %q\n%s", w, body)
		}
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

// TestAcquireSkipsDeadTunnel: туннель убитого коннектора не достаётся клиенту.
// Раньше он выдавался, RoundTrip падал на записи в мёртвое соединение и клиент
// получал 502; теперь такой туннель выбрасывается из пула, а ответ — 503.
func TestAcquireSkipsDeadTunnel(t *testing.T) {
	m := metrics.New()
	r := NewRegistry(m)
	registerDeadBound(t, r, "llama3:8b")

	tunnel, ok := r.Acquire("llama3:8b")
	if ok || tunnel != nil {
		t.Fatalf("Acquire() = (%v, %v), want (nil, false): dead tunnel must not be given out", tunnel, ok)
	}

	if pending, ready := r.counts(); pending != 0 || ready != 0 {
		t.Fatalf("counts() = (pending %d, ready %d), want (0, 0)", pending, ready)
	}

	assertMetrics(t, m,
		`centaur_gateway_tunnel_stale_total{state="ready"} 1`,
		`centaur_gateway_tunnels{state="ready"} 0`,
		`centaur_gateway_tunnel_acquire_misses_total 1`,
	)
}

// TestAcquireUsesLiveTunnelAmongDead: мёртвые туннели не мешают выдаче живого —
// они выбрасываются по ходу поиска, как бы ни обходилась карта.
func TestAcquireUsesLiveTunnelAmongDead(t *testing.T) {
	m := metrics.New()
	r := NewRegistry(m)
	registerDeadBound(t, r, "llama3:8b", "qwen2.5:7b")
	registerDeadBound(t, r, "llama3:8b")
	registerBound(t, r, "llama3:8b")

	tunnel, ok := r.Acquire("llama3:8b")
	if !ok {
		t.Fatal("Acquire(llama3:8b) = false, want live tunnel")
	}
	if !slices.Contains(tunnel.Models, "llama3:8b") {
		t.Fatalf("Acquire() models = %v, want contain llama3:8b", tunnel.Models)
	}

	// Модель есть только у мёртвых туннелей — честный отказ.
	if tunnel, ok := r.Acquire("qwen2.5:7b"); ok {
		t.Fatalf("Acquire(qwen2.5:7b) = (%v, true), want (nil, false)", tunnel)
	}

	assertMetrics(t, m,
		`centaur_gateway_tunnel_stale_total{state="ready"} 2`,
		`centaur_gateway_tunnels{state="ready"} 0`,
	)
}

// TestSweepRemovesDeadAndExpiredTunnels: свип вычищает и мёртвые соединения, и
// регистрации, до handshake которых коннектор не дожил (pendingTTL).
func TestSweepRemovesDeadAndExpiredTunnels(t *testing.T) {
	m := metrics.New()
	r := NewRegistry(m)

	registerDeadBound(t, r, "llama3:8b")
	registerBound(t, r, "llama3:8b")
	stale := r.Register([]string{"llama3:8b"})
	expire(t, r, stale)

	if dropped := r.Sweep(); dropped != 2 {
		t.Fatalf("Sweep() = %d, want 2", dropped)
	}

	if pending, ready := r.counts(); pending != 0 || ready != 1 {
		t.Fatalf("counts() = (pending %d, ready %d), want (0, 1)", pending, ready)
	}

	assertMetrics(t, m,
		`centaur_gateway_tunnel_stale_total{state="ready"} 1`,
		`centaur_gateway_tunnel_stale_total{state="pending"} 1`,
		`centaur_gateway_tunnels{state="ready"} 1`,
	)
}

// TestSweepKeepsLiveTunnels: живые туннели свип не трогает, счётчик вычистки не
// появляется.
func TestSweepKeepsLiveTunnels(t *testing.T) {
	m := metrics.New()
	r := NewRegistry(m)

	registerBound(t, r, "llama3:8b")
	r.Register([]string{"llama3:8b"})

	if dropped := r.Sweep(); dropped != 0 {
		t.Fatalf("Sweep() = %d, want 0", dropped)
	}

	if pending, ready := r.counts(); pending != 1 || ready != 1 {
		t.Fatalf("counts() = (pending %d, ready %d), want (1, 1)", pending, ready)
	}
	if body := scrape(t, m); strings.Contains(body, "centaur_gateway_tunnel_stale_total") {
		t.Errorf("stale counter must not appear for live tunnels\n%s", body)
	}
}

// TestSweepConcurrentWithAcquire: свип и выдача идут под одним мьютексом реестра
// — гонок нет (проверяется прогоном с -race).
func TestSweepConcurrentWithAcquire(t *testing.T) {
	r := NewRegistry(metrics.New())
	for range 4 {
		registerBound(t, r, "llama3:8b")
		registerDeadBound(t, r, "llama3:8b")
	}

	wg := sync.WaitGroup{}
	wg.Go(func() {
		for range 10 {
			r.Sweep()
		}
	})
	wg.Go(func() {
		for range 10 {
			r.Acquire("llama3:8b")
		}
	})
	wg.Wait()
}
