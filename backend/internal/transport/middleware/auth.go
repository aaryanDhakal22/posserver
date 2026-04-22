package middleware

import (
	"crypto/subtle"
	"errors"
	"net/http"

	authSvc "quiccpos/main/internal/app/auth"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// APIKeyAuth protects routes behind an X-API-Key header matched against the
// DB-persisted key set via POST /api/v1/auth/key.
func APIKeyAuth(svc *authSvc.Service) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			log := zerolog.Ctx(c.Request().Context())

			apiKey := c.Request().Header.Get("X-API-Key")
			if apiKey == "" {
				log.Warn().Str("path", c.Request().URL.Path).Msg("auth: missing X-API-Key header")
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing X-API-Key header"})
			}

			storedKey, err := svc.GetKey(c.Request().Context())
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					log.Warn().Msg("auth: API key not configured")
					return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "API key not configured"})
				}
				log.Error().Err(err).Msg("auth: failed to retrieve API key")
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "auth check failed"})
			}

			if subtle.ConstantTimeCompare([]byte(apiKey), []byte(storedKey)) != 1 {
				log.Warn().Str("path", c.Request().URL.Path).Msg("auth: invalid API key")
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid API key"})
			}

			return next(c)
		}
	}
}
