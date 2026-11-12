package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// AdminAuth gates the /admin/v1/* surface with a single shared bearer token
// read from ADMIN_API_KEY at startup. Constant-time compare so a slow scanner
// can't extract the key one byte at a time.
//
// Accepts either `Authorization: Bearer <key>` or `X-Admin-Key: <key>`. The
// dev UI uses the Bearer form so it can be tested with curl unmodified.
func AdminAuth(expected string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if expected == "" {
				zerolog.Ctx(c.Request().Context()).Warn().Msg("admin: ADMIN_API_KEY unset; refusing all requests")
				return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "admin key not configured"})
			}
			got := bearerOrHeader(c.Request().Header.Get("Authorization"), c.Request().Header.Get("X-Admin-Key"))
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
				zerolog.Ctx(c.Request().Context()).Warn().Str("path", c.Request().URL.Path).Msg("admin: invalid key")
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid admin key"})
			}
			c.SetRequest(c.Request().WithContext(withAdmin(c.Request().Context()))) // the audit log's actor
			return next(c)
		}
	}
}

// IsAdminRequest reports whether r carries the admin key (either header
// form), for routes that are open on dev and admin-only elsewhere.
func IsAdminRequest(r *http.Request, expected string) bool {
	got := bearerOrHeader(r.Header.Get("Authorization"), r.Header.Get("X-Admin-Key"))
	return expected != "" && got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

func bearerOrHeader(authz, xAdmin string) string {
	if xAdmin != "" {
		return xAdmin
	}
	if strings.HasPrefix(authz, "Bearer ") {
		return strings.TrimPrefix(authz, "Bearer ")
	}
	return ""
}
