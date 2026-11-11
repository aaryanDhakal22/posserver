// Package kitchen answers "can this store's kitchen get an order right now?"
// (DURABILITY-PLAN.md: main refuses a channel order when the store's Pi has
// been silent ~30 s — if the kitchen can't get it, we don't take it).
//
// The signal is the Pi's own live connection to main: its SSE stream
// (/api/v1/events/orders). Orders reach the kitchen only over that stream, so
// it is exactly the thing that must work. A stream that dropped is "away"
// from the moment it closed; a store never connected since main started is
// away since main started (a Pi reconnects within seconds of a main restart).
// A stream can stay open on a dead link (TCP half-open) until main's
// keep-alive write fails, which can take minutes, so the Pi's printer report
// (an RPC it pushes every ~30 s) backs it up: an open stream whose last report
// is older than ReportStale counts as away too. A store with no agent key
// (no Pi set up — dev and test stores) isn't checked.
package kitchen

import (
	"context"
	"time"

	sseBroker "quiccpos/main/internal/infra/sse"

	"github.com/google/uuid"
)

// Presence is the SSE broker's view of a scope's streams.
type Presence interface {
	Presence(scope sseBroker.Scope) (streams int, awaySince time.Time)
}

// Reports is the printer-report cache: how long ago the Pi last reported.
type Reports interface {
	Age(scope sseBroker.Scope) (time.Duration, bool)
}

// Agents counts a location's agent keys that aren't revoked.
type Agents interface {
	ActiveAgents(ctx context.Context, locationID uuid.UUID) (int64, error)
}

var (
	// SilentFor: a Pi without a stream this long is offline.
	SilentFor = 30 * time.Second
	// ReportStale: an open stream whose Pi hasn't reported this long is
	// treated as a dead link (the Pi reports every ~30 s).
	ReportStale = 90 * time.Second
)

type Liveness struct {
	presence Presence
	reports  Reports
	agents   Agents
	now      func() time.Time
}

func New(p Presence, r Reports, a Agents) *Liveness {
	return &Liveness{presence: p, reports: r, agents: a, now: time.Now}
}

// Offline reports whether the store's kitchen (its Pi) can't get an order now,
// and since when it has been away (zero when it is online or unchecked).
func (l *Liveness) Offline(ctx context.Context, tenantID, locationID uuid.UUID) (bool, time.Time, error) {
	n, err := l.agents.ActiveAgents(ctx, locationID)
	if err != nil {
		return false, time.Time{}, err
	}
	if n == 0 {
		return false, time.Time{}, nil // no Pi set up: nothing to check
	}
	scope := sseBroker.Scope{TenantID: tenantID, LocationID: locationID}
	streams, away := l.presence.Presence(scope)
	if streams > 0 {
		if l.reports != nil {
			if age, ok := l.reports.Age(scope); ok && age > ReportStale {
				return true, l.now().Add(-age), nil
			}
		}
		return false, time.Time{}, nil
	}
	if l.now().Sub(away) >= SilentFor {
		return true, away, nil
	}
	return false, time.Time{}, nil
}
