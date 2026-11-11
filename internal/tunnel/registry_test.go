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

// newTCPConn returns a pair of loopback connections: Registry.Bind takes exactly
// *net.TCPConn, so a fake net.Conn will not do.
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

// registerBound registers a tunnel and binds a connection to it — this is how a
// tunnel looks in the registry after the connector's handshake.
func registerBound(t *testing.T, r *Registry, models ...string) {
	t.Helper()

	id := r.Register(models)
	conn, _ := newTCPConn(t)
	if err := r.Bind(id, conn); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
}

// registerDeadBound registers a tunnel whose connector has already died: the
// client half of the connection is closed and we wait for EOF on the server
// half. After EOF the liveness probe always sees a dead connection — there is no
// race with FIN delivery.
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

// expire moves a registration into the past: this way pendingTTL is tested
// without waiting.
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

// waitEmptyRegistry waits for the registry to become empty (no longer than timeout).
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

// scrape serves the metrics in the Prometheus text format.
func scrape(t *testing.T, m *metrics.Metrics) string {
	t.Helper()

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want %d", rec.Code, http.StatusOK)
	}
	return rec.Body.String()
}

// assertMetrics checks that the metrics output contains all the expected lines.
func assertMetrics(t *testing.T, m *metrics.Metrics, want ...string) {
	t.Helper()

	body := scrape(t, m)
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("metrics output does not contain %q\n%s", w, body)
		}
	}
}

// TestAcquireRoutesByModel checks model-based routing: a tunnel supporting the
// requested model is handed out and immediately disappears from the registry.
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

	// A tunnel is single-use: it is not handed out again, and there are no other
	// tunnels with this model.
	if _, ok := r.Acquire("qwen2.5:7b"); ok {
		t.Fatal("Acquire(qwen2.5:7b) = true, want false: tunnel must be removed")
	}

	// A tunnel for another model is untouched.
	tunnel, ok = r.Acquire("llama3:8b")
	if !ok {
		t.Fatal("Acquire(llama3:8b) = false, want tunnel")
	}
	if !slices.Contains(tunnel.Models, "llama3:8b") {
		t.Fatalf("Acquire() models = %v, want contain llama3:8b", tunnel.Models)
	}

	// No tunnel has a connector for this model.
	if _, ok := r.Acquire("unknown"); ok {
		t.Fatal("Acquire(unknown) = true, want false")
	}
}

// TestAcquireEmptyRegistryReturnsImmediately: an empty registry is an instant
// refusal. The threshold is well below the former 2-second wait: the test fails
// if Acquire ever goes back to polling for a free tunnel.
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

// TestAcquireSkipsUnboundTunnel: a tunnel that reached the registry before Bind
// (Conn == nil) is not handed out — otherwise RoundTrip would write to a
// connection that is not ready.
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

// TestAcquireSkipsDeadTunnel: a tunnel of a killed connector is not handed to a
// client. It used to be handed out, RoundTrip failed writing to the dead
// connection and the client got a 502; now such a tunnel is dropped from the pool
// and the response is a 503.
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

// TestAcquireUsesLiveTunnelAmongDead: dead tunnels do not prevent a live one
// from being handed out — they are dropped as the search proceeds, however the
// map happens to be traversed.
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

	// The model is only served by dead tunnels — an honest refusal.
	if tunnel, ok := r.Acquire("qwen2.5:7b"); ok {
		t.Fatalf("Acquire(qwen2.5:7b) = (%v, true), want (nil, false)", tunnel)
	}

	assertMetrics(t, m,
		`centaur_gateway_tunnel_stale_total{state="ready"} 2`,
		`centaur_gateway_tunnels{state="ready"} 0`,
	)
}

// TestSweepRemovesDeadAndExpiredTunnels: the sweep drops both dead connections
// and registrations whose connector never made it to the handshake (pendingTTL).
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

// TestSweepKeepsLiveTunnels: the sweep leaves live tunnels alone and the
// cleanup counter does not appear.
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

// TestSweepConcurrentWithAcquire: the sweep and acquiring run under the same
// registry mutex — no races (verified by running with -race).
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
