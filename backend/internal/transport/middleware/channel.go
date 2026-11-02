package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	channelApp "quiccpos/main/internal/app/channel"
	operatorApp "quiccpos/main/internal/app/operator"
	channelDom "quiccpos/main/internal/domain/channel"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// channelCtxKey namespaces the channel on the request context (its own auth
// path, beside device keys, store sessions and operator tokens).
type channelCtxKey int

const (
	channelKey channelCtxKey = iota
	channelOperatorKey
)

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
			var op *channelDom.Operator
			var ch *channelDom.Channel
			var err error
			if strings.HasPrefix(key, operatorApp.SessionPrefix) {
				// An operator's sign-in: the store they work on → its
				// built-in Call center channel (main 3.0 step 7).
				loc, perr := uuid.Parse(c.Request().Header.Get("X-Location-ID"))
				if perr != nil {
					return c.JSON(http.StatusBadRequest, map[string]string{"error": "send the store you work on as X-Location-ID", "code": "location_required"})
				}
				ch, op, err = svc.AuthenticateOperator(c.Request().Context(), key, loc)
				if errors.Is(err, channelApp.ErrLocationNotAllowed) {
					return c.JSON(http.StatusForbidden, map[string]string{"error": err.Error(), "code": "location_not_allowed"})
				}
			} else {
				ch, err = svc.Authenticate(c.Request().Context(), key)
			}
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
			if op != nil {
				ctx = context.WithValue(ctx, channelOperatorKey, *op)
				trace.SpanFromContext(ctx).SetAttributes(attribute.String("operator.id", op.ID.String()))
			}
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

// ChannelOperatorFromContext is the operator signed in to an operator
// channel (none for a channel key).
func ChannelOperatorFromContext(ctx context.Context) (channelDom.Operator, bool) {
	op, ok := ctx.Value(channelOperatorKey).(channelDom.Operator)
	return op, ok
}
