// Package touch bounds the "last used" stamps written on every authed
// request (device keys, channel keys, platform keys, operator tokens): at
// most one write per id per Every, each in its own goroutine with a 5 s
// deadline, detached from the request. Before this every request started an
// unbounded goroutine with context.Background().
package touch

import (
	"context"
	"sync"
	"time"

	"quiccpos/main/internal/shared/safego"

	"github.com/google/uuid"
)

// Every is how often one id's stamp is written at most.
const Every = time.Minute

// Timeout bounds one write.
const Timeout = 5 * time.Second

// maxIDs caps the remembered ids; past it, entries older than Every are
// dropped (they would be allowed again anyway).
const maxIDs = 10_000

// Limiter remembers when each id was last stamped.
type Limiter struct {
	mu    sync.Mutex
	last  map[uuid.UUID]time.Time
	every time.Duration
	now   func() time.Time
}

// New makes a Limiter allowing one stamp per id per every.
func New(every time.Duration) *Limiter {
	return &Limiter{last: map[uuid.UUID]time.Time{}, every: every, now: time.Now}
}

// Allow reports whether id may be stamped now (and records it if so).
func (l *Limiter) Allow(id uuid.UUID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if t, ok := l.last[id]; ok && now.Sub(t) < l.every {
		return false
	}
	if len(l.last) >= maxIDs {
		for k, t := range l.last {
			if now.Sub(t) >= l.every {
				delete(l.last, k)
			}
		}
	}
	l.last[id] = now
	return true
}

// Do runs write(ctx) for id in a goroutine with a Timeout deadline, unless
// id was stamped within the last period. Returns whether it started one.
func (l *Limiter) Do(id uuid.UUID, write func(ctx context.Context) error) bool {
	if !l.Allow(id) {
		return false
	}
	safego.Go(context.Background(), "touch", func(context.Context) {
		ctx, cancel := context.WithTimeout(context.Background(), Timeout)
		defer cancel()
		_ = write(ctx)
	})
	return true
}
