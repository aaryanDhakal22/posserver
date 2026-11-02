package middleware

import (
	"context"
	"errors"
	"net/http"

	channelApp "quiccpos/main/internal/app/channel"
	channelDom "quiccpos/main/internal/domain/channel"

	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// channelCtxKey namespaces the channel on the request context (its own auth
// path, beside device keys, store sessions and operator tokens).
type channelCtxKey int

const channelKey channelCtxKey = 0

// ChannelAuth gates /channel/v1 with the channel's bearer key (qch_…). No
// caching: a revoke or a new key takes effect on the next request. Errors are
// {error, code} like every /channel/v1 answer.
func ChannelAuth(svc *channelApp.Service) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			key := bearerOrHeader(c.Request().Header.Get("Authorization"), "")
			if key == "" {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing channel key", "code": "unauthorized"})
			}
			ch, err := svc.Authenticate(c.Request().Context(), key)
			if errors.Is(err, channelApp.ErrInvalidKey) {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid or revoked channel key", "code": "unauthorized"})
			}
			if err != nil { // the lookup failed (database), not the key
				return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "channel lookup is unavailable, try again", "code": "try_again"})
			}
			trace.SpanFromContext(c.Request().Context()).SetAttributes(
				attribute.String("auth.kind", "channel"),
				attribute.String("channel.id", ch.ID.String()),
				attribute.String("channel.kind", string(ch.Kind)),
				attribute.String("tenant.id", ch.TenantID.String()),
				attribute.String("location.id", ch.LocationID.String()),
			)
			ctx := context.WithValue(c.Request().Context(), channelKey, *ch)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

// ChannelFromContext returns the authenticated channel.
func ChannelFromContext(ctx context.Context) (channelDom.Channel, bool) {
	ch, ok := ctx.Value(channelKey).(channelDom.Channel)
	return ch, ok
}
