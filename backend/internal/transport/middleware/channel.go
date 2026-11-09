package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	channelApp "quiccpos/main/internal/app/channel"
	operatorApp "quiccpos/main/internal/app/operator"
	channelDom "quiccpos/main/internal/domain/channel"
	"quiccpos/main/internal/domain/customer"
	siteDom "quiccpos/main/internal/domain/site"

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

// ChannelAuth gates /channel/v1 with the channel's bearer key (qch_…), an
// operator's sign-in (qops_… + X-Location-ID) or the storefront's platform
// key (qpk_… + X-Site-Host → the store's built-in web channel). No
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
			} else if strings.HasPrefix(key, siteDom.KeyPrefix) {
				// The storefront's platform key + the host it serves →
				// that store's built-in web channel (weborder stage 2).
				ch, err = svc.AuthenticateSite(c.Request().Context(), key, c.Request().Header.Get("X-Site-Host"))
				switch {
				case errors.Is(err, channelApp.ErrHostRequired):
					return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error(), "code": "host_required"})
				case errors.Is(err, channelApp.ErrUnknownSite):
					return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error(), "code": "unknown_site"})
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

// --- customer sessions (weborder stage 2) ---------------------------------------------

type customerCtxKey struct{}

// CustomerAuth reads a store's customer session from X-Customer-Session on
// a web channel request (after ChannelAuth). Without the header the request
// is a guest's; required = the route needs a signed-in customer (401
// sign_in). A bad / expired session is 401 session_invalid.
func CustomerAuth(auth func(ctx context.Context, locationID uuid.UUID, raw string) (customer.Customer, error), required bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			ch, ok := ChannelFromContext(c.Request().Context())
			if !ok {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing channel key", "code": "unauthorized"})
			}
			raw := strings.TrimSpace(c.Request().Header.Get("X-Customer-Session"))
			if raw == "" {
				if required {
					return c.JSON(http.StatusUnauthorized, map[string]string{"error": "sign in first", "code": "sign_in"})
				}
				return next(c)
			}
			if ch.Kind != channelDom.KindWeb {
				return c.JSON(http.StatusForbidden, map[string]string{"error": "customer accounts are for the store's website", "code": "forbidden"})
			}
			cu, err := auth(c.Request().Context(), ch.LocationID, raw)
			if errors.Is(err, customer.ErrBadSession) {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": err.Error(), "code": "session_invalid"})
			}
			if err != nil {
				return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "sign-in lookup is unavailable, try again", "code": "try_again"})
			}
			trace.SpanFromContext(c.Request().Context()).SetAttributes(attribute.String("customer.id", cu.ID.String()))
			c.SetRequest(c.Request().WithContext(context.WithValue(c.Request().Context(), customerCtxKey{}, cu)))
			return next(c)
		}
	}
}

// CustomerFromContext is the signed-in customer (none for a guest).
func CustomerFromContext(ctx context.Context) (customer.Customer, bool) {
	cu, ok := ctx.Value(customerCtxKey{}).(customer.Customer)
	return cu, ok
}
