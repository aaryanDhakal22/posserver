package middleware

import (
	"context"
	"errors"
	"net/http"

	authSvc "quiccpos/main/internal/app/auth"
	domainAuth "quiccpos/main/internal/domain/auth"
	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/observability"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type ctxKey int

const (
	deviceKey ctxKey = iota

	// StoreAPIKeyHeader is the single header every device-authenticated
	// caller (agent SSE, agent REST polls, mobile, future ingest sources)
	// presents. The header's value is the cleartext key minted by the admin
	// `/admin/v1/devices` endpoint; we hash it and look the device up in the
	// `devices` table. The "STORE" naming reflects the scope — one key
	// authenticates one (tenant, location) device pair, and a store has many
	// devices.
	StoreAPIKeyHeader = "X-STORE-API-KEY"
)

// APIKeyAuth hashes the X-STORE-API-KEY header, looks up the active device,
// and attaches the resolved Device to both the request context (so handlers
// can pull (tenant_id, location_id) from it) and the current span (so traces
// + logs filter on scope in Grafana).
func APIKeyAuth(svc *authSvc.Service, meters observability.Meters) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			ctx := c.Request().Context()
			log := zerolog.Ctx(ctx)

			apiKey := c.Request().Header.Get(StoreAPIKeyHeader)
			if apiKey == "" {
				meters.AuthStoreDenied.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", "missing_key")))
				log.Warn().Str("path", c.Request().URL.Path).Msg("auth: missing X-STORE-API-KEY header")
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing " + StoreAPIKeyHeader + " header"})
			}

			dev, err := svc.Authenticate(ctx, apiKey)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					meters.AuthStoreDenied.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", "invalid_key")))
					log.Warn().Str("path", c.Request().URL.Path).Msg("auth: unknown or revoked key")
					return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid API key"})
				}
				meters.AuthStoreDenied.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", "lookup_error")))
				log.Error().Err(err).Msg("auth: lookup failed")
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "auth check failed"})
			}
			meters.AuthStoreAllowed.Add(ctx, 1)

			// Annotate span + log with scope so every downstream record
			// filters on tenant/location in Grafana without parsing the body.
			span := trace.SpanFromContext(c.Request().Context())
			span.SetAttributes(
				attribute.String("tenant.id", dev.TenantID.String()),
				attribute.String("location.id", dev.LocationID.String()),
				attribute.String("device.id", dev.ID.String()),
				attribute.String("device.kind", dev.Kind),
			)
			subLog := log.With().
				Str("tenant_id", dev.TenantID.String()).
				Str("location_id", dev.LocationID.String()).
				Str("device_id", dev.ID.String()).
				Logger()

			ctx = context.WithValue(c.Request().Context(), deviceKey, dev)
			ctx = subLog.WithContext(ctx)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

// DeviceFromContext returns the authenticated Device. Returns nil if the
// context was not produced by APIKeyAuth — handlers behind the middleware
// can assume non-nil.
// RequireDeviceKind runs after APIKeyAuth and lets only devices of `kind`
// through (403 otherwise). The agent-only surface (the order SSE stream, the
// duplex RPC, store-config, self-revoke) uses it so a manager-minted mobile /
// kiosk key can't consume the agent's order stream (which would mark orders
// dispatched without the agent ever printing them) or post floor events.
func RequireDeviceKind(kind string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			d := DeviceFromContext(c.Request().Context())
			if d == nil || d.Kind != kind {
				return c.JSON(http.StatusForbidden, map[string]string{"error": "this key is not an agent key"})
			}
			return next(c)
		}
	}
}

func DeviceFromContext(ctx context.Context) *domainAuth.Device {
	d, _ := ctx.Value(deviceKey).(*domainAuth.Device)
	return d
}

// ScopeFromContext extracts the order.Scope for the authenticated request.
// Zero-value scope is returned for unauthenticated requests — handlers should
// never call this without the middleware in front of them.
func ScopeFromContext(ctx context.Context) order.Scope {
	d := DeviceFromContext(ctx)
	if d == nil {
		return order.Scope{}
	}
	return order.Scope{
		TenantID:   order.TenantID(d.TenantID),
		LocationID: order.LocationID(d.LocationID),
	}
}

// IsZeroUUID is a small predicate shared by handlers that need to distinguish
// "auth middleware did not run" from "device legitimately resolves to the
// nil UUID" (which never happens, but explicit is better than implicit).
func IsZeroUUID(u uuid.UUID) bool { return u == uuid.Nil }
