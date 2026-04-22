package middleware

import (
	"fmt"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// RequestLogger attaches a request-scoped zerolog logger to the context and
// logs request start/end with method, path, status, and duration.
func RequestLogger(logger *zerolog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			start := time.Now()

			requestID := req.Header.Get("X-Request-ID")
			if requestID == "" {
				requestID = fmt.Sprintf("%d", start.UnixNano())
			}
			c.Response().Header().Set("X-Request-ID", requestID)

			reqLog := logger.With().
				Str("request_id", requestID).
				Str("method", req.Method).
				Str("path", req.URL.Path).
				Str("remote_ip", c.RealIP()).
				Logger()

			ctx := reqLog.WithContext(req.Context())
			c.SetRequest(req.WithContext(ctx))

			reqLog.Info().Msg("request started")

			err := next(c)

			_, status := echo.ResolveResponseStatus(c.Response(), err)
			durationMs := time.Since(start).Milliseconds()

			logFn := reqLog.Info()
			if status >= 500 {
				logFn = reqLog.Error()
			}

			logFn.
				Int("status", status).
				Int64("duration_ms", durationMs).
				Msg("request completed")

			return err
		}
	}
}
