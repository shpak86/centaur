package tunnel

import (
	"context"
	"crypto/rand"
	"sync"
	"time"
)

type TokenManager struct {
	free   map[string]time.Time
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.RWMutex
}

func NewTokenManager() *TokenManager {
	t := TokenManager{
		free: make(map[string]time.Time),
	}
	t.ctx, t.cancel = context.WithCancel(context.Background())
	return &t
}

func (tm *TokenManager) Serve() {
	t := time.NewTicker(time.Second)
	for {
		select {
		case <-tm.ctx.Done():
			t.Stop()
		case <-t.C:
			tm.mu.Lock()
			for token, ts := range tm.free {
				if time.Since(ts) > time.Minute {
					delete(tm.free, token)
				}
			}
			tm.mu.Unlock()
		}
	}
}

func (tm *TokenManager) Close() {
	tm.cancel()
}

func (tm *TokenManager) Get() string {
	token := rand.Text()[:16]
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.free[token] = time.Now()
	return token
}

func (tm *TokenManager) Acquire(token string) bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	_, isFree := tm.free[token]
	if isFree {
		delete(tm.free, token)
	}
	return isFree
}
