package middleware

import (
	"context"
	"errors"
	"net/http"

	siteApp "quiccpos/main/internal/app/site"
	siteDom "quiccpos/main/internal/domain/site"

	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// PlatformAuth gates a route the storefront calls as itself, for no store in
// particular (no X-Site-Host): its platform key (qpk_…) alone. Today that is
// only the Square OAuth relay, where the signed state names the store. No
// caching: a revoked key is refused on the next request.
func PlatformAuth(auth func(ctx context.Context, key string) (siteDom.PlatformKey, error)) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			key := bearerOrHeader(c.Request().Header.Get("Authorization"), "")
			if key == "" || auth == nil {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing platform key", "code": "unauthorized"})
			}
			k, err := auth(c.Request().Context(), key)
			if errors.Is(err, siteApp.ErrInvalidKey) {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid or revoked platform key", "code": "unauthorized"})
			}
			if err != nil {
				return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "platform key lookup is unavailable, try again", "code": "try_again"})
			}
			trace.SpanFromContext(c.Request().Context()).SetAttributes(
				attribute.String("auth.kind", "platform"),
				attribute.String("platform_key.id", k.ID.String()),
			)
			return next(c)
		}
	}
}
