// Package metrics собирает и отдаёт Prometheus-метрики gateway: клиентские
// HTTP-запросы, регистрации/привязки туннелей и состояние пула.
package metrics

import (
	"errors"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// durationBuckets покрывают диапазон от мгновенных ручек (health) до длинных
// генераций Ollama, включая стримы на минуты.
var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300}

// Metrics владеет отдельным реестром Prometheus: так несколько экземпляров
// (например, в тестах) не конфликтуют за глобальный реестр и не падают на
// повторной регистрации одной и той же метрики.
type Metrics struct {
	registry *prometheus.Registry

	// RequestsTotal — клиентские запросы по эндпоинту и HTTP-статусу.
	RequestsTotal *prometheus.CounterVec
	// RequestDuration — время обработки запроса gateway, включая стриминг.
	RequestDuration *prometheus.HistogramVec
	// TunnelRegistrations — сколько туннелей удалось зарегистрировать.
	TunnelRegistrations prometheus.Counter
	// TunnelBindErrors — ошибки привязки соединения к туннелю.
	TunnelBindErrors prometheus.Counter
	// TunnelAcquireMisses — запросы, которым не досталось свободного туннеля.
	TunnelAcquireMisses prometheus.Counter
	// TunnelStale — туннели, вычищенные из пула по состоянию (ready —
	// соединение мертво, pending — handshake не завершён).
	TunnelStale *prometheus.CounterVec
}

// New создаёт реестр метрик вместе с коллекторами рантайма Go и процесса.
func New() *Metrics {
	m := Metrics{registry: prometheus.NewRegistry()}

	m.RequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "centaur",
		Subsystem: "gateway",
		Name:      "requests_total",
		Help:      "Клиентские запросы, обработанные gateway, по эндпоинту и HTTP-статусу.",
	}, []string{"endpoint", "code"})

	m.RequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "centaur",
		Subsystem: "gateway",
		Name:      "request_duration_seconds",
		Help:      "Длительность обработки клиентского запроса gateway в секундах.",
		Buckets:   durationBuckets,
	}, []string{"endpoint"})

	m.TunnelRegistrations = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "centaur",
		Subsystem: "gateway",
		Name:      "tunnel_registrations_total",
		Help:      "Туннели, зарегистрированные коннекторами в gateway.",
	})

	m.TunnelBindErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "centaur",
		Subsystem: "gateway",
		Name:      "tunnel_bind_errors_total",
		Help:      "Ошибки привязки соединения к туннелю (неизвестный id или пустое соединение).",
	})

	m.TunnelAcquireMisses = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "centaur",
		Subsystem: "gateway",
		Name:      "tunnel_acquire_misses_total",
		Help:      "Запросы, для которых не нашлось свободного туннеля (ответ 503).",
	})

	m.TunnelStale = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "centaur",
		Subsystem: "gateway",
		Name:      "tunnel_stale_total",
		Help:      "Туннели, вычищенные из пула: соединение мертво (ready) или handshake не завершён (pending).",
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

// Register добавляет внешний коллектор (например, состояние пула туннелей).
// Повторная регистрация того же самого коллектора игнорируется, чтобы не
// паниковать на рестарте/двойной инициализации.
func (m *Metrics) Register(collector prometheus.Collector) {
	if err := m.registry.Register(collector); err != nil {
		var already prometheus.AlreadyRegisteredError
		if errors.As(err, &already) {
			return
		}
		panic(err)
	}
}

// Handler отдаёт метрики в текстовом формате Prometheus.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
