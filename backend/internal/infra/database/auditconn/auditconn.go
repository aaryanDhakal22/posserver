// Package auditconn tells Postgres who is acting, so the audit triggers
// (audit_row, baseline 2026-11-12) can record it: before a pooled connection
// is handed to a request, its session settings quicc.actor (JSON) and
// quicc.trace (the request's trace id) are made to match the request. A
// connection already carrying the same values costs nothing; otherwise one
// small round trip. Work without a request (workers, loops) runs as
// "system" (both settings empty).
package auditconn

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace"
)

// Actor is who a request acts as. Kind: admin | manager | device | operator |
// channel | customer | platform (system = no actor).
type Actor struct {
	Kind           string
	ID             string
	Name           string
	TenantID       string
	LocationID     string
	ImpersonatedBy string
	IP             string
	RequestID      string
}

// JSON is the quicc.actor value the triggers read (short keys: it rides on
// every connection hand-out that changes actor).
func (a Actor) JSON() string {
	m := map[string]string{"k": a.Kind}
	put := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	put("id", a.ID)
	put("n", a.Name)
	put("t", a.TenantID)
	put("l", a.LocationID)
	put("by", a.ImpersonatedBy)
	put("ip", a.IP)
	put("rid", a.RequestID)
	b, _ := json.Marshal(m)
	return string(b)
}

const (
	keyActor = "quicc.audit.actor"
	keyTrace = "quicc.audit.trace"
	setSQL   = `SELECT set_config('quicc.actor', $1, false), set_config('quicc.trace', $2, false)`
)

// Settings is what a connection must carry for ctx: the actor JSON and the
// trace id ("" and "" when actorOf finds nobody).
func Settings(ctx context.Context, actorOf func(context.Context) (Actor, bool)) (actor, traceID string) {
	a, ok := actorOf(ctx)
	if !ok {
		return "", ""
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		traceID = sc.TraceID().String()
	}
	return a.JSON(), traceID
}

// Install sets cfg.PrepareConn (keeping one already set, which runs first).
func Install(cfg *pgxpool.Config, actorOf func(context.Context) (Actor, bool)) {
	prev := cfg.PrepareConn
	cfg.PrepareConn = func(ctx context.Context, conn *pgx.Conn) (bool, error) {
		if prev != nil {
			if ok, err := prev(ctx, conn); !ok || err != nil {
				return ok, err
			}
		}
		actor, traceID := Settings(ctx, actorOf)
		data := conn.PgConn().CustomData()
		if data[keyActor] == actor && data[keyTrace] == traceID {
			return true, nil
		}
		// PgConn-level: no tracer span per hand-out.
		res := conn.PgConn().ExecParams(ctx, setSQL, [][]byte{[]byte(actor), []byte(traceID)}, nil, nil, nil)
		if _, err := res.Close(); err != nil {
			// A broken connection: the pool drops it and hands out another.
			delete(data, keyActor)
			return false, nil
		}
		// The empty strings are cached too: a fresh connection has no setting,
		// which the triggers read the same as "".
		data[keyActor], data[keyTrace] = actor, traceID
		return true, nil
	}
}

// Entry is main's own audit row for one admin / manager write
// (middleware.AuditWrites → repositories.AuditRepository).
type Entry struct {
	Actor    Actor
	Method   string
	Route    string
	EntityID string
	Params   map[string]string
	Status   int
	TraceID  string
	At       time.Time
}
