package tunnel

import "github.com/prometheus/client_golang/prometheus"

// tunnelStateDesc describes the centaur_gateway_tunnels{state} gauge: how many
// tunnels are currently in the registry in the pending (awaiting handshake) and
// ready (able to accept a request) states.
var tunnelStateDesc = prometheus.NewDesc(
	prometheus.BuildFQName("centaur", "gateway", "tunnels"),
	"Tunnels registered in the gateway by state: pending or ready.",
	[]string{"state"},
	nil,
)

// tunnelStateCollector computes the pool state at collection time under the
// registry mutex, so the metric cannot drift because of connection drops and
// always matches the registry contents.
type tunnelStateCollector struct {
	registry *Registry
}

func newTunnelStateCollector(r *Registry) *tunnelStateCollector {
	return &tunnelStateCollector{registry: r}
}

func (c *tunnelStateCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- tunnelStateDesc
}

func (c *tunnelStateCollector) Collect(ch chan<- prometheus.Metric) {
	pending, ready := c.registry.counts()
	ch <- prometheus.MustNewConstMetric(tunnelStateDesc, prometheus.GaugeValue, float64(pending), statePending)
	ch <- prometheus.MustNewConstMetric(tunnelStateDesc, prometheus.GaugeValue, float64(ready), stateReady)
}
