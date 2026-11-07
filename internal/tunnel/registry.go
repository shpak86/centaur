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
)

// Registry хранит зарегистрированные туннели и выдаёт их по одному на запрос.
// Туннели одноразовые: при выдаче туннель удаляется из реестра, а после ответа
// соединение закрывается и коннектор регистрирует новый. Возврата в реестр нет.
type Registry struct {
	tunnels map[string]*Tunnel
	mu      sync.Mutex
}

func NewRegistry() *Registry {
	return &Registry{
		tunnels: make(map[string]*Tunnel),
	}
}

func (r *Registry) Register(models []string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := rand.Text()[:16]
	tunnel := NewTunnel(id, nil, models)
	slog.Debug("Register tunnel", "id", id, "models", models)
	r.tunnels[id] = tunnel
	return id
}

// Bind привязывает соединение коннектора к зарегистрированному туннелю.
func (r *Registry) Bind(id string, conn *net.TCPConn) error {
	if conn == nil {
		return errors.New("bind closed connection")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	tunnel, exists := r.tunnels[id]
	if !exists {
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

	return nil, false
}
