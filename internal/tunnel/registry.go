package tunnel

import (
	"context"
	"slices"
	"sync"
	"time"
)

const (
	// acquireTimeout — сколько ждём свободный туннель. Коннектор занимает
	// несколько миллисекунд на переподключение, поэтому сразу после
	// обслуживания запроса реестр может быть пуст: второй запрос того же
	// keep-alive соединения не должен получать 503 из-за этой гонки.
	acquireTimeout = 2 * time.Second
	// acquirePollInterval — шаг опроса реестра при ожидании.
	acquirePollInterval = 5 * time.Millisecond
)

// Registry хранит зарегистрированные туннели и выдаёт их по одному на запрос.
// Туннели одноразовые: после ответа они удаляются из реестра и закрываются,
// а коннектор регистрирует новый. Поэтому возврата туннеля в реестр нет —
// освобождение это Delete.
type Registry struct {
	tunnels []*Tunnel
	mu      sync.Mutex
}

func NewRegistry() *Registry {
	return &Registry{
		tunnels: make([]*Tunnel, 0),
	}
}

// Add добавляет туннель в реестр. nil и уже добавленный туннель игнорируются.
func (r *Registry) Add(t *Tunnel) {
	if t == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if slices.Contains(r.tunnels, t) {
		return
	}

	r.tunnels = append(r.tunnels, t)
}

// Tunnels возвращает копию списка туннелей, включая занятые: вызывающая
// сторона может обходить его (Stop закрывает соединения), пока реестр
// меняется.
func (r *Registry) Tunnels() []*Tunnel {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.tunnels)
}

// Delete удаляет туннель из реестра и снимает с него резервирование.
// Сравнение идёт по указателю, поэтому удалять нужно тот же экземпляр,
// который вернул Acquire (или принимал Add).
func (r *Registry) Delete(t *Tunnel) {
	if t == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	idx := slices.Index(r.tunnels, t)
	if idx == -1 {
		return
	}

	r.tunnels = slices.Delete(r.tunnels, idx, idx+1)
}

// take резервирует первый свободный туннель (Busy = true).
func (r *Registry) take() (*Tunnel, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, t := range r.tunnels {
		if t.Busy {
			continue
		}

		t.Busy = true
		return t, true
	}

	return nil, false
}

// Acquire резервирует свободный туннель, ожидая его не дольше acquireTimeout.
// Если туннеля так и не появилось (или отменился контекст запроса) — ok == false.
func (r *Registry) Acquire(ctx context.Context) (*Tunnel, bool) {
	deadline := time.Now().Add(acquireTimeout)

	for {
		if t, ok := r.take(); ok {
			return t, true
		}

		if time.Now().After(deadline) {
			return nil, false
		}

		timer := time.NewTimer(acquirePollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false
		case <-timer.C:
		}
	}
}
