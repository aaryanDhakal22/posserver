package middleware

import (
	"context"
	"strings"

	"github.com/labstack/echo/v5"
)

// Drain ends long-lived streams when the server shuts down (HARDENING-PLAN
// F-15): http.Server.Shutdown waits for active requests, and an SSE stream
// never finishes on its own, so every shutdown used to wait the whole
// graceful timeout and then cut the connections. Close (registered with
// http.Server.RegisterOnShutdown) cancels the context of every stream route
// (a path ending in /events or containing /events/: the agent's
// /api/v1/events/orders and /channel/v1/events), so their handlers return
// and Shutdown drains the ordinary requests normally.
type Drain struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// NewDrain makes an open Drain.
func NewDrain() *Drain {
	ctx, cancel := context.WithCancel(context.Background())
	return &Drain{ctx: ctx, cancel: cancel}
}

// Close ends every stream (idempotent).
func (d *Drain) Close() { d.cancel() }

// IsStream reports whether a route template is a long-lived stream.
func IsStream(route string) bool {
	return strings.HasSuffix(route, "/events") || strings.Contains(route, "/events/")
}

// Middleware gives stream routes a context that ends with the Drain.
func (d *Drain) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if !IsStream(c.Path()) {
				return next(c)
			}
			req := c.Request()
			ctx, cancel := context.WithCancel(req.Context())
			stop := context.AfterFunc(d.ctx, cancel)
			defer stop()
			defer cancel()
			c.SetRequest(req.WithContext(ctx))
			return next(c)
		}
	}
}
