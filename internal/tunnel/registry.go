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

	"github.com/shpak86/centaur/internal/metrics"
)

// Registry хранит зарегистрированные туннели и выдаёт их по одному на запрос.
// Туннели одноразовые: при выдаче туннель удаляется из реестра, а после ответа
// соединение закрывается и коннектор регистрирует новый. Возврата в реестр нет.
type Registry struct {
	tunnels map[string]*Tunnel
	metrics *metrics.Metrics
	mu      sync.Mutex
}

// NewRegistry создаёт реестр туннелей. m может быть nil — тогда реестр работает
// без метрик (например, в точечных тестах); состояние пула в таком случае не
// экспортируется.
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

// Bind привязывает соединение коннектора к зарегистрированному туннелю.
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

// Tunnels отдаёт итератор по текущему содержимому реестра. Занятые туннели
// в него не попадают: они удаляются при выдаче из Acquire.
func (r *Registry) Tunnels() iter.Seq[*Tunnel] {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Values(r.tunnels)
}

// Acquire отдаёт туннель, поддерживающий model, и сразу удаляет его из реестра:
// туннель одноразовый, повторного обращения к нему не будет.
// Туннель попадает в реестр до Bind, поэтому пока Conn == nil, он пропускается:
// отдать его нельзя — RoundTrip писал бы в неготовое соединение.
// Подходящего туннеля нет — (nil, false): ждать нечего, честнее сразу
// ответить 503, чем держать запрос в таймауте.
func (r *Registry) Acquire(model string) (*Tunnel, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for id, t := range r.tunnels {
		if t.Conn == nil {
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

// recordBindError увеличивает счётчик ошибок привязки, если метрики включены.
func (r *Registry) recordBindError() {
	if r.metrics != nil {
		r.metrics.TunnelBindErrors.Inc()
	}
}

// counts возвращает число туннелей в состоянии pending (Conn == nil, ждём
// handshake) и ready (есть соединение). Используется коллектором состояния пула.
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
