package middleware

import (
	"context"
	"net/http"

	operatorApp "quiccpos/main/internal/app/operator"
	operatorDom "quiccpos/main/internal/domain/operator"
	"quiccpos/main/internal/observability"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// operatorCtxKey namespaces the operator identity on the request context (kept
// distinct from deviceKey + storeScopeKey — three independent auth paths).
type operatorCtxKey int

const operatorKey operatorCtxKey = 0

// OperatorContext is the resolved operator + its allowed locations.
type OperatorContext struct {
	OperatorID uuid.UUID
	Username   string
	Allowed    []operatorDom.AllowedLocation
}

// OperatorAuth gates the read-only operator surface with the bearer token. No
// caching → revocation is immediate.
func OperatorAuth(svc *operatorApp.Service, meters observability.Meters) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			ctx := c.Request().Context()
			token := bearerOrHeader(c.Request().Header.Get("Authorization"), "")
			if token == "" {
				meters.AuthOperatorDenied.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", "missing_token")))
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing bearer token"})
			}
			op, allowed, err := svc.AuthenticateToken(ctx, token)
			if err != nil {
				meters.AuthOperatorDenied.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", "invalid_token")))
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid or expired token"})
			}
			meters.AuthOperatorAllowed.Add(ctx, 1)
			span := trace.SpanFromContext(c.Request().Context())
			span.SetAttributes(
				attribute.String("operator.id", op.ID.String()),
				attribute.String("auth.kind", "operator"),
			)
			ctx = context.WithValue(c.Request().Context(), operatorKey, OperatorContext{OperatorID: op.ID, Username: op.Username, Allowed: allowed})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

func OperatorFromContext(ctx context.Context) (OperatorContext, bool) {
	o, ok := ctx.Value(operatorKey).(OperatorContext)
	return o, ok
}

// OperatorAllowsLocation reports whether the operator may read the location.
func OperatorAllowsLocation(ctx context.Context, locationID uuid.UUID) bool {
	o, ok := OperatorFromContext(ctx)
	if !ok {
		return false
	}
	for _, a := range o.Allowed {
		if a.LocationID == locationID {
			return true
		}
	}
	return false
}
