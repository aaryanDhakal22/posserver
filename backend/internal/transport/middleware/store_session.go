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

const storeScopeKey storeCtxKey = 0

// StoreSessionAuth gates the /store/v1/me/* + session-whoami surface with the
// signed session cookie. On success it stashes the manager's order.Scope on the
// context (so handlers read (tenant, location) from the cookie, never the URL)
// and annotates the span — mirroring APIKeyAuth so traces/logs filter on scope.
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
			log := zerolog.Ctx(c.Request().Context()).With().
				Str("tenant_id", claims.TenantID.String()).
				Str("location_id", claims.LocationID.String()).
				Logger()

			ctx := context.WithValue(c.Request().Context(), storeScopeKey, scope)
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
