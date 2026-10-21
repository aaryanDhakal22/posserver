package middleware

import (
	"context"
	"net/http"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/transport/session"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// storeCtxKey namespaces the store-manager session scope on the request context
// (kept distinct from auth.go's deviceKey so the two auth paths never collide).
type storeCtxKey int

const (
	storeScopeKey storeCtxKey = iota
	storeImpersonatorKey
)

// StoreSessionAuth gates the /store/v1/me/* + session-whoami surface with the
// signed session cookie. On success it stashes the manager's order.Scope on the
// context (so handlers read (tenant, location) from the cookie, never the URL)
// and annotates the span — mirroring APIKeyAuth so traces/logs filter on scope.
// An admin open-as-store session also stamps auth.impersonated_by on the span,
// impersonated_by on the log context, and the label on the request context.
func StoreSessionAuth(signer *session.Signer) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			claims, ok := signer.Verify(c.Request())
			if !ok {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "login required"})
			}

			scope := order.Scope{
				TenantID:   order.TenantID(claims.TenantID),
				LocationID: order.LocationID(claims.LocationID),
			}

			span := trace.SpanFromContext(c.Request().Context())
			span.SetAttributes(
				attribute.String("tenant.id", claims.TenantID.String()),
				attribute.String("location.id", claims.LocationID.String()),
				attribute.String("auth.kind", "store-session"),
			)
			logCtx := zerolog.Ctx(c.Request().Context()).With().
				Str("tenant_id", claims.TenantID.String()).
				Str("location_id", claims.LocationID.String())
			ctx := context.WithValue(c.Request().Context(), storeScopeKey, scope)
			if claims.ImpersonatedBy != "" {
				span.SetAttributes(attribute.String("auth.impersonated_by", claims.ImpersonatedBy))
				logCtx = logCtx.Str("impersonated_by", claims.ImpersonatedBy)
				ctx = context.WithValue(ctx, storeImpersonatorKey, claims.ImpersonatedBy)
			}
			log := logCtx.Logger()
			ctx = log.WithContext(ctx)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

// StoreScopeFromContext returns the session scope stashed by StoreSessionAuth.
// ok is false when the middleware did not run (handlers behind it can assume true).
func StoreScopeFromContext(ctx context.Context) (order.Scope, bool) {
	s, ok := ctx.Value(storeScopeKey).(order.Scope)
	return s, ok
}

// StoreImpersonatorFromContext returns who opened this store session on the
// manager's behalf ("admin"), or "" for the manager's own login.
func StoreImpersonatorFromContext(ctx context.Context) string {
	by, _ := ctx.Value(storeImpersonatorKey).(string)
	return by
}
