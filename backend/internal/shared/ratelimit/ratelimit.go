// Package ratelimit is a small in-memory sliding-window counter, keyed by an
// arbitrary string. main runs as one replica (blue-green swaps, never two
// live), so in-process state is enough; a restart forgets the windows.
//
// Two uses: agentsync counts FAILED link attempts (Add only on failure, Reset
// on success); diag counts every accepted report (Add on success) across
// several windows at once.
package ratelimit

import (
	"sync"
	"time"
)

// maxKeys bounds memory under key spraying (random usernames): past it, Add
// first drops every expired entry.
const maxKeys = 10_000

// Window allows `max` events per key within `window`.
type Window struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string][]time.Time
	now    func() time.Time
}

func New(max int, window time.Duration) *Window {
	return &Window{max: max, window: window, hits: map[string][]time.Time{}, now: time.Now}
}

// SetClock swaps the time source (tests).
func (w *Window) SetClock(now func() time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.now = now
}

func (w *Window) prune(key string, now time.Time) []time.Time {
	var keep []time.Time
	for _, t := range w.hits[key] {
		if now.Sub(t) < w.window {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(w.hits, key)
	} else {
		w.hits[key] = keep
	}
	return keep
}

// Allow reports whether one more event for key fits under the window's max.
func (w *Window) Allow(key string) bool { return w.AllowN(key, w.max) }

// AllowN is Allow with a per-call ceiling (a global key shares the window but
// has its own max).
func (w *Window) AllowN(key string, max int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.prune(key, w.now())) < max
}

// RetryAfter is how long until key has room again under the window's max
// (0 when it has room now).
func (w *Window) RetryAfter(key string) time.Duration { return w.RetryAfterN(key, w.max) }

// RetryAfterN is RetryAfter with a per-call ceiling.
func (w *Window) RetryAfterN(key string, max int) time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	hits := w.prune(key, now)
	if len(hits) < max {
		return 0
	}
	// The (len-max)th oldest hit has to age out before there is room.
	return hits[len(hits)-max].Add(w.window).Sub(now)
}

// Add records one event for key.
func (w *Window) Add(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	if len(w.hits) > maxKeys {
		for k := range w.hits {
			w.prune(k, now)
		}
	}
	w.hits[key] = append(w.prune(key, now), now)
}

// Reset forgets key.
func (w *Window) Reset(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.hits, key)
}
