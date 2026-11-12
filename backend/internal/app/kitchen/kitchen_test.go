package kitchen

import (
	"context"
	"testing"
	"time"

	sseBroker "quiccpos/main/internal/infra/sse"

	"github.com/google/uuid"
)

type fakePresence struct {
	n    int
	away time.Time
}

func (f fakePresence) Presence(sseBroker.Scope) (int, time.Time) { return f.n, f.away }

type fakeReports struct {
	age time.Duration
	ok  bool
}

func (f fakeReports) Age(sseBroker.Scope) (time.Duration, bool) { return f.age, f.ok }

type fakeAgents int64

func (f fakeAgents) ActiveAgents(context.Context, uuid.UUID) (int64, error) { return int64(f), nil }

func TestOffline(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		agents  int64
		p       fakePresence
		r       fakeReports
		offline bool
	}{
		{"no Pi set up: never checked", 0, fakePresence{0, now.Add(-time.Hour)}, fakeReports{}, false},
		{"connected, no report yet (main just started)", 1, fakePresence{1, time.Time{}}, fakeReports{}, false},
		{"connected, fresh report", 1, fakePresence{1, time.Time{}}, fakeReports{20 * time.Second, true}, false},
		{"connected, report too old (a dead link)", 1, fakePresence{1, time.Time{}}, fakeReports{2 * time.Minute, true}, true},
		{"gone 10 s: a reconnect, not offline", 1, fakePresence{0, now.Add(-10 * time.Second)}, fakeReports{}, false},
		{"gone 30 s", 1, fakePresence{0, now.Add(-30 * time.Second)}, fakeReports{}, true},
	}
	for _, c := range cases {
		l := New(c.p, c.r, fakeAgents(c.agents))
		l.now = func() time.Time { return now }
		off, _, err := l.Offline(context.Background(), uuid.New(), uuid.New())
		if err != nil || off != c.offline {
			t.Errorf("%s: offline = %v (%v), want %v", c.name, off, err, c.offline)
		}
	}
}
