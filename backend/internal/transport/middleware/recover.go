package middleware

import (
	"fmt"
	"net/http"
	"runtime/debug"

	"quiccpos/main/internal/shared/safego"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// Recover turns a handler panic into a 500 and logs it at error with its
// stack, on the request's logger (request_id, path, trace_id, span_id) —
// HARDENING-PLAN L-3: echo's own Recover put the stack only into the error
// value, which RequestLogger never printed. Counted as main.panics{where=http}.
// Install it inside RequestLogger (AddDefaultMiddlewares) so the request
// logger is in the context. The client gets a plain 500: never the stack.
func Recover(fallback *zerolog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) (err error) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				ctx := c.Request().Context()
				lg := zerolog.Ctx(ctx)
				if lg.GetLevel() == zerolog.Disabled && fallback != nil {
					lg = fallback
				}
				safego.Count(ctx, "http")
				lg.Error().Ctx(ctx).
					Str("route", c.Path()).
					Str("panic", fmt.Sprint(rec)).
					Str("stack", string(debug.Stack())).
					Msg("panic in HTTP handler")
				err = echo.ErrInternalServerError
			}()
			return next(c)
		}
	}
}
