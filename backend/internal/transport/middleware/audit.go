package middleware

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"

	"quiccpos/main/internal/infra/database/auditconn"
)

// The audit log (baseline 2026-11-12, contract §2.5zg): the database triggers
// record every money / staff / settings / menu row change with the actor
// AuditActor finds on the request; AuditWrites adds main's own row for every
// admin and manager write (the route and its answer).

type requestInfoKeyT struct{}
type adminKeyT struct{}

type requestInfo struct{ id, ip string }

// withRequestInfo is set by RequestLogger (the request id it answers with and
// the caller's address).
func withRequestInfo(ctx context.Context, id, ip string) context.Context {
	return context.WithValue(ctx, requestInfoKeyT{}, requestInfo{id: id, ip: ip})
}

// withAdmin marks a request AdminAuth let through.
func withAdmin(ctx context.Context) context.Context {
	return context.WithValue(ctx, adminKeyT{}, true)
}

// AuditActor is who the request acts as, for the audit triggers
// (auditconn.Install in cmd/api) and AuditWrites. ok=false: no
// authenticated caller (workers, loops, open routes) = "system".
func AuditActor(ctx context.Context) (auditconn.Actor, bool) {
	var a auditconn.Actor
	ri, _ := ctx.Value(requestInfoKeyT{}).(requestInfo)
	a.RequestID, a.IP = ri.id, ri.ip
	switch {
	case DeviceFromContext(ctx) != nil:
		d := DeviceFromContext(ctx)
		a.Kind, a.ID, a.Name = "device", d.ID.String(), d.Name
		a.TenantID, a.LocationID = d.TenantID.String(), d.LocationID.String()
	case storeScopeSet(ctx):
		s, _ := StoreScopeFromContext(ctx)
		a.Kind, a.ID = "manager", uuid.UUID(s.TenantID).String()
		a.TenantID, a.LocationID = uuid.UUID(s.TenantID).String(), uuid.UUID(s.LocationID).String()
		if by := StoreImpersonatorFromContext(ctx); by != "" {
			a.Kind, a.ImpersonatedBy = "admin", by
		}
	case ctx.Value(adminKeyT{}) != nil:
		a.Kind = "admin"
	default:
		if cc, ok := CallcenterFromContext(ctx); ok {
			// a call-center browser; the agent signed in on it rides in the name
			a.Kind, a.ID, a.Name = "callcenter", cc.DeviceID, cc.DeviceName
			if cc.StaffName != "" {
				a.Name = cc.DeviceName + " / " + cc.StaffName
			}
			a.TenantID, a.LocationID = cc.TenantID, cc.LocationID
		} else if op, ok := OperatorFromContext(ctx); ok {
			a.Kind, a.ID, a.Name = "operator", op.OperatorID.String(), op.Username
		} else if ch, ok := ChannelFromContext(ctx); ok {
			a.Kind, a.ID, a.Name = "channel", ch.ID.String(), ch.Name
			a.TenantID, a.LocationID = ch.TenantID.String(), ch.LocationID.String()
			if cu, ok := CustomerFromContext(ctx); ok {
				a.Kind, a.ID, a.Name = "customer", cu.ID.String(), ""
			} else if op, ok := ChannelOperatorFromContext(ctx); ok {
				a.Kind, a.ID, a.Name = "operator", op.ID.String(), op.Username
			}
		} else {
			return a, false
		}
	}
	return a, true
}

func storeScopeSet(ctx context.Context) bool {
	_, ok := StoreScopeFromContext(ctx)
	return ok
}

// AuditEntry is main's own audit row for one admin / manager write.
type AuditEntry = auditconn.Entry

// AuditWriter stores AuditEntry rows (repositories.AuditRepository).
type AuditWriter interface {
	WriteApp(ctx context.Context, e AuditEntry) error
}

// AuditWrites records every write (POST / PUT / PATCH / DELETE) through the
// routes it guards, after the handler answered, refused ones included. Put it
// after the auth middleware (it reads the actor auth set). A failed write is
// logged, never the request's failure. w nil = off.
func AuditWrites(w AuditWriter) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			if w == nil || req.Method == http.MethodGet || req.Method == http.MethodHead || req.Method == http.MethodOptions {
				return next(c)
			}
			start := time.Now()
			err := next(c)
			_, status := echo.ResolveResponseStatus(c.Response(), err)
			ctx := req.Context()
			actor, _ := AuditActor(ctx)
			_, traceID := auditconn.Settings(ctx, func(context.Context) (auditconn.Actor, bool) { return actor, true })
			params := map[string]string{}
			for _, p := range c.PathValues() {
				params[p.Name] = p.Value
			}
			e := AuditEntry{Actor: actor, Method: req.Method, Route: c.Path(), EntityID: c.Param("id"),
				Params: params, Status: status, TraceID: traceID, At: start}
			wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			if werr := w.WriteApp(wctx, e); werr != nil {
				zerolog.Ctx(ctx).Warn().Err(werr).Str("route", e.Route).Msg("audit: app row not written")
			}
			return err
		}
	}
}

// Chain runs a before b (b sees what a put on the request).
func Chain(a, b echo.MiddlewareFunc) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc { return a(b(next)) }
}
