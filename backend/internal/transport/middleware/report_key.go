package middleware

import (
	"context"
	"errors"
	"net/http"

	"quiccpos/main/internal/app/diag"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// ReportKeyHeader carries a Quibase tablet's write-only report token. It is a
// fourth, separate auth path: the token lives in its own table
// (tablet_report_tokens), is checked only by ReportKeyAuth, and ReportKeyAuth
// guards exactly one route (POST /store/v1/tablet/diag-reports). It is never
// accepted as an X-STORE-API-KEY, so it cannot read orders or anything else.
const ReportKeyHeader = "X-Quibase-Report-Key"

// reportKeyCtxKey namespaces the tablet identity on the request context (kept
// distinct from deviceKey / operatorKey / storeScopeKey).
type reportKeyCtxKey int

const tabletKey reportKeyCtxKey = 0

// ReportKeyAuth resolves X-Quibase-Report-Key to the tablet + its store scope.
// 401 when missing, unknown or revoked.
func ReportKeyAuth(svc *diag.Service) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			ctx := c.Request().Context()
			log := zerolog.Ctx(ctx)
			token := c.Request().Header.Get(ReportKeyHeader)
			if token == "" {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing " + ReportKeyHeader + " header"})
			}
			tab, err := svc.Authenticate(ctx, token)
			if errors.Is(err, diag.ErrInvalidToken) {
				log.Warn().Str("path", c.Request().URL.Path).Msg("report key: unknown or revoked")
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid report key", "code": "invalid_report_key"})
			}
			if err != nil {
				log.Error().Err(err).Msg("report key: lookup failed")
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "auth check failed"})
			}
			trace.SpanFromContext(ctx).SetAttributes(
				attribute.String("tenant.id", tab.Scope.TenantID.String()),
				attribute.String("location.id", tab.Scope.LocationID.String()),
				attribute.String("auth.kind", "tablet_report"),
			)
			subLog := log.With().
				Str("tenant_id", tab.Scope.TenantID.String()).
				Str("location_id", tab.Scope.LocationID.String()).
				Str("tablet_id", tab.TabletID).
				Logger()
			ctx = context.WithValue(ctx, tabletKey, tab)
			ctx = subLog.WithContext(ctx)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

// TabletFromContext returns the tablet ReportKeyAuth resolved (nil otherwise).
func TabletFromContext(ctx context.Context) *diag.TabletAuth {
	t, _ := ctx.Value(tabletKey).(*diag.TabletAuth)
	return t
}
