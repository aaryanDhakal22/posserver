package diag

import (
	"sync"
	"time"

	"quiccpos/main/internal/shared/ratelimit"

	"github.com/google/uuid"
)

// Report rate limits. A tablet's token maps 1:1 to its tablet id, so the
// per-tablet window is the per-token limit for direct sends and applies the
// same way to reports relayed by the Pi.
const (
	perTabletMax    = 30 // per hour
	perLocationMax  = 120
	globalMax       = 1000 // per hour
	manualWindow    = 10 * time.Minute
	globalKey       = "*"
	perLocationSpan = 24 * time.Hour
)

type limits struct {
	mu       sync.Mutex // check-all-then-add-all is one step
	tablet   *ratelimit.Window
	location *ratelimit.Window
	global   *ratelimit.Window
	manual   *ratelimit.Window
}

func newLimits() *limits {
	return &limits{
		tablet:   ratelimit.New(perTabletMax, time.Hour),
		location: ratelimit.New(perLocationMax, perLocationSpan),
		global:   ratelimit.New(globalMax, time.Hour),
		manual:   ratelimit.New(1, manualWindow),
	}
}

func (l *limits) setClock(now func() time.Time) {
	for _, w := range []*ratelimit.Window{l.tablet, l.location, l.global, l.manual} {
		w.SetClock(now)
	}
}

// take books one report, or returns how long to wait (>0) when any window is
// full — then nothing is booked.
func (l *limits) take(location uuid.UUID, tabletID, kind string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	tabKey := location.String() + "/" + tabletID
	locKey := location.String()

	var wait time.Duration
	longest := func(d time.Duration) {
		if d > wait {
			wait = d
		}
	}
	longest(l.tablet.RetryAfter(tabKey))
	longest(l.location.RetryAfter(locKey))
	longest(l.global.RetryAfter(globalKey))
	if kind == "manual" {
		longest(l.manual.RetryAfter(tabKey))
	}
	if wait > 0 {
		return wait
	}
	l.tablet.Add(tabKey)
	l.location.Add(locKey)
	l.global.Add(globalKey)
	if kind == "manual" {
		l.manual.Add(tabKey)
	}
	return 0
}
