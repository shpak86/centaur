// Package metrics collects and exposes the gateway's Prometheus metrics:
// client HTTP requests, tunnel registrations/bindings, and the pool state.
package metrics

import (
	"errors"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// durationBuckets cover the range from instant endpoints (health) to long Ollama
// generations, including streams lasting minutes.
var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300}

// Metrics owns a dedicated Prometheus registry: this way multiple instances
// (e.g. in tests) do not contend for the global registry or panic on registering
// the same metric twice.
type Metrics struct {
	registry *prometheus.Registry

	// RequestsTotal counts client requests by endpoint and HTTP status.
	RequestsTotal *prometheus.CounterVec
	// RequestDuration measures gateway request handling time, including
	// streaming.
	RequestDuration *prometheus.HistogramVec
	// TunnelRegistrations counts tunnels successfully registered.
	TunnelRegistrations prometheus.Counter
	// TunnelBindErrors counts connection-to-tunnel binding errors.
	TunnelBindErrors prometheus.Counter
	// TunnelAcquireMisses counts requests that did not get a free tunnel.
	TunnelAcquireMisses prometheus.Counter
	// TunnelStale counts tunnels swept from the pool by state (ready — the
	// connection is dead, pending — the handshake never completed).
	TunnelStale *prometheus.CounterVec
}

// New creates the metrics registry along with the Go runtime and process
// collectors.
func New() *Metrics {
	m := Metrics{registry: prometheus.NewRegistry()}

	m.RequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "centaur",
		Subsystem: "gateway",
		Name:      "requests_total",
		Help:      "Client requests handled by the gateway, by endpoint and HTTP status.",
	}, []string{"endpoint", "code"})

	m.RequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "centaur",
		Subsystem: "gateway",
		Name:      "request_duration_seconds",
		Help:      "Gateway client request handling duration in seconds.",
		Buckets:   durationBuckets,
	}, []string{"endpoint"})

	m.TunnelRegistrations = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "centaur",
		Subsystem: "gateway",
		Name:      "tunnel_registrations_total",
		Help:      "Tunnels registered by connectors in the gateway.",
	})

	m.TunnelBindErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "centaur",
		Subsystem: "gateway",
		Name:      "tunnel_bind_errors_total",
		Help:      "Errors binding a connection to a tunnel (unknown id or empty connection).",
	})

	m.TunnelAcquireMisses = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "centaur",
		Subsystem: "gateway",
		Name:      "tunnel_acquire_misses_total",
		Help:      "Requests for which no free tunnel was available (503 response).",
	})

	m.TunnelStale = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "centaur",
		Subsystem: "gateway",
		Name:      "tunnel_stale_total",
		Help:      "Tunnels swept from the pool: the connection is dead (ready) or the handshake never completed (pending).",
	}, []string{"state"})

	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.RequestsTotal,
		m.RequestDuration,
		m.TunnelRegistrations,
		m.TunnelBindErrors,
		m.TunnelAcquireMisses,
		m.TunnelStale,
	)
	return &m
}

// Register adds an external collector (e.g. the tunnel pool state). Registering
// the same collector twice is ignored so that a restart or double initialization
// does not panic.
func (m *Metrics) Register(collector prometheus.Collector) {
	if err := m.registry.Register(collector); err != nil {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) {
			return
		}
		panic(err)
	}
}

// Handler serves the metrics in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
