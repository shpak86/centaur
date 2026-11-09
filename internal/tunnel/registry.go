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

// Состояния туннеля в реестре: pending — зарегистрирован, но соединение ещё не
// привязано (ждём handshake), ready — соединение есть, туннель готов принять
// запрос. Те же значения использует метрика centaur_gateway_tunnels{state}.
const (
	statePending = "pending"
	stateReady   = "ready"
)

// pendingTTL — сколько регистрация может ждать handshake. Коннектор дожимает
// регистрацию и dial в одной функции (см. internal/tunnel/client.go), поэтому
// запас здесь заведомо больше суммы dialTimeout и retryDelay: туннель без
// соединения так долго живёт только если коннектор упал между этими шагами.
// Если TTL всё же сработает раньше Bind, привязка вернёт ошибку, коннектор
// повторит регистрацию — самоисправление ценой bind_errors.
const pendingTTL = 30 * time.Second

// Registry хранит зарегистрированные туннели и выдаёт их по одному на запрос.
// Туннели одноразовые: при выдаче туннель удаляется из реестра, а после ответа
// соединение закрывается и коннектор регистрирует новый. Возврата в реестр нет.
//
// Мёртвых туннелей реестр за собой не оставляет: при выдаче соединение
// проверяется на живость (Tunnel.alive), а Sweep периодически вычищает то, что
// осталось без трафика. Иначе туннель убитого коннектора числится ready и
// выдаётся клиенту, а тот получает 502 на запись в мёртвое соединение.
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
// Мёртвые туннели (коннектор упал или закрыл соединение) выбрасываются по ходу
// поиска: клиенту нельзя отдать соединение, в которое уже некому писать, —
// вместо 502 он получит 503 и повторит запрос.
// Подходящего туннеля нет — (nil, false): ждать нечего, честнее сразу
// ответить 503, чем держать запрос в таймауте.
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

// Sweep вычищает из реестра туннели, которых уже нет: соединения, закрытые
// коннектором (в том числе после его падения), и регистрации, до handshake
// которых коннектор не дожил. Вызывается периодически из Service: при нулевом
// трафике Acquire не срабатывает, и без свипа мёртвый туннель занимал бы место
// в пуле, а метрика состояния пула врала бы.
// Возвращает число выброшенных туннелей — для лога.
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

// drop удаляет туннель из реестра (вызывается под r.mu) и закрывает его
// соединение, если оно было, — так освобождается дескриптор.
func (r *Registry) drop(id string, t *Tunnel) {
	if t.Conn != nil {
		if err := t.Conn.Close(); err != nil {
			slog.Debug("Close stale tunnel", "id", id, "error", err)
		}
	}
	delete(r.tunnels, id)
}

// recordStale увеличивает счётчик выброшенных туннелей, если метрики включены.
func (r *Registry) recordStale(state string) {
	if r.metrics != nil {
		r.metrics.TunnelStale.WithLabelValues(state).Inc()
	}
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
