package tunnel

import "github.com/prometheus/client_golang/prometheus"

// tunnelStateDesc описывает gauge centaur_gateway_tunnels{state}: сколько
// туннелей сейчас в реестре в состоянии pending (ждём handshake) и ready
// (готов принять запрос).
var tunnelStateDesc = prometheus.NewDesc(
	prometheus.BuildFQName("centaur", "gateway", "tunnels"),
	"Туннели, зарегистрированные в gateway, по состоянию: pending или ready.",
	[]string{"state"},
	nil,
)

// tunnelStateCollector считает состояние пула прямо на момент сбора под
// мьютексом реестра: так метрика не «плывёт» из-за обрывов соединений и
// всегда совпадает с содержимым реестра.
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
	ch <- prometheus.MustNewConstMetric(tunnelStateDesc, prometheus.GaugeValue, float64(pending), "pending")
	ch <- prometheus.MustNewConstMetric(tunnelStateDesc, prometheus.GaugeValue, float64(ready), "ready")
}
