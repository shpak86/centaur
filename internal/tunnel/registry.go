package tunnel

import (
	"crypto/rand"
	"errors"
	"iter"
	"log/slog"
	"maps"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/shpak86/centaur/internal/metrics"
)

// Tunnel states in the registry: pending — registered but the connection is not
// bound yet (awaiting handshake), ready — the connection exists and the tunnel
// is ready to accept a request. The centaur_gateway_tunnels{state} metric uses
// the same values.
const (
	statePending = "pending"
	stateReady   = "ready"
)

// pendingTTL is how long a registration may wait for the handshake. The
// connector performs registration and dial in a single function (see
// internal/tunnel/client.go), so the margin here is deliberately larger than
// the sum of dialTimeout and retryDelay: a tunnel without a connection lives
// this long only if the connector crashed between those steps. If the TTL does
// fire before Bind, the binding returns an error and the connector retries the
// registration — self-healing at the cost of bind_errors.
const pendingTTL = 30 * time.Second

// Registry stores registered tunnels and hands them out one per request.
// Tunnels are single-use: on hand-out the tunnel is removed from the registry,
// and after the response the connection is closed and the connector registers a
// new one. There is no return to the registry.
//
// The registry does not leave dead tunnels behind: on hand-out the connection
// is checked for liveness (Tunnel.alive), and Sweep periodically removes what
// has seen no traffic. Otherwise a dead connector's tunnel would count as ready
// and be handed to a client, which would then get a 502 writing to a dead
// connection.
type Registry struct {
	tunnels map[string]*Tunnel
	metrics *metrics.Metrics
	mu      sync.Mutex
}

// NewRegistry creates a tunnel registry. m may be nil — the registry then works
// without metrics (e.g. in focused tests), and the pool state is not exported.
func NewRegistry(m *metrics.Metrics) *Registry {
	r := &Registry{
		tunnels: make(map[string]*Tunnel),
		metrics: m,
	}
	if m != nil {
		m.Register(newTunnelStateCollector(r))
	}
	return r
}

// Register records a new tunnel for the given models and returns its id. The
// tunnel starts in the pending state until Bind attaches a connection.
func (r *Registry) Register(models []string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := rand.Text()[:16]
	tunnel := NewTunnel(id, nil, models)
	slog.Debug("Register tunnel", "id", id, "models", models)
	r.tunnels[id] = tunnel
	if r.metrics != nil {
		r.metrics.TunnelRegistrations.Inc()
	}
	return id
}

// Bind attaches a connector's connection to the registered tunnel.
func (r *Registry) Bind(id string, conn *net.TCPConn) error {
	if conn == nil {
		r.recordBindError()
		return errors.New("bind closed connection")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	tunnel, exists := r.tunnels[id]
	if !exists {
		r.recordBindError()
		return errors.New("unable to bind tunnel id: " + id)
	}
	tunnel.Conn = conn
	r.tunnels[id] = tunnel
	slog.Debug("Bind conn", "id", id, "conn", conn.RemoteAddr().String())
	return nil
}

// Tunnels returns an iterator over the current registry contents. Tunnels that
// are in use do not appear: they are removed on hand-out by Acquire.
func (r *Registry) Tunnels() iter.Seq[*Tunnel] {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Values(r.tunnels)
}

// Acquire hands out a tunnel that supports model and immediately removes it from
// the registry: tunnels are single-use, so it will never be requested again.
// A tunnel enters the registry before Bind, so while Conn == nil it is skipped:
// it cannot be handed out — RoundTrip would write to a connection that is not
// ready.
// Dead tunnels (the connector crashed or closed the connection) are dropped
// during the search: a connection nobody can write to must not reach a client —
// instead of a 502 it gets a 503 and retries the request.
// If no matching tunnel exists, the result is (nil, false): there is nothing to
// wait for, and it is more honest to answer 503 right away than to keep the
// request waiting until a timeout.
func (r *Registry) Acquire(model string) (*Tunnel, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for id, t := range r.tunnels {
		if t.Conn == nil {
			continue
		}
		if !t.alive() {
			slog.Info("Drop dead tunnel", "id", id, "models", t.Models)
			r.recordStale(stateReady)
			r.drop(id, t)
			continue
		}
		if slices.Contains(t.Models, model) {
			delete(r.tunnels, id)
			return t, true
		}
	}

	if r.metrics != nil {
		r.metrics.TunnelAcquireMisses.Inc()
	}
	return nil, false
}

// Sweep removes tunnels that no longer exist from the registry: connections the
// connector closed (including after a crash) and registrations whose handshake
// never happened. It is called periodically by Service: with no traffic Acquire
// never runs, and without the sweep a dead tunnel would occupy a pool slot while
// the pool state metric lied.
// It returns the number of dropped tunnels — for logging.
func (r *Registry) Sweep() (dropped int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for id, t := range r.tunnels {
		var state string
		switch {
		case t.Conn == nil && time.Since(t.RegisteredAt) > pendingTTL:
			state = statePending
		case t.Conn != nil && !t.alive():
			state = stateReady
		default:
			continue
		}

		slog.Info("Drop stale tunnel", "id", id, "state", state, "models", t.Models)
		r.recordStale(state)
		r.drop(id, t)
		dropped++
	}

	return dropped
}

// drop removes the tunnel from the registry (called under r.mu) and closes its
// connection, if any, to release the descriptor.
func (r *Registry) drop(id string, t *Tunnel) {
	if t.Conn != nil {
		if err := t.Conn.Close(); err != nil {
			slog.Debug("Close stale tunnel", "id", id, "error", err)
		}
	}
	delete(r.tunnels, id)
}

// recordStale increments the dropped-tunnel counter when metrics are enabled.
func (r *Registry) recordStale(state string) {
	if r.metrics != nil {
		r.metrics.TunnelStale.WithLabelValues(state).Inc()
	}
}

// recordBindError increments the bind-error counter when metrics are enabled.
func (r *Registry) recordBindError() {
	if r.metrics != nil {
		r.metrics.TunnelBindErrors.Inc()
	}
}

// counts returns the number of tunnels in the pending state (Conn == nil,
// awaiting handshake) and the ready state (connection present). Used by the pool
// state collector.
func (r *Registry) counts() (pending, ready int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, t := range r.tunnels {
		if t.Conn == nil {
			pending++
			continue
		}
		ready++
	}
	return pending, ready
}
