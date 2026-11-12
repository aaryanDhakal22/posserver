package middleware

import (
	"github.com/labstack/echo/v5"
	echomw "github.com/labstack/echo/v5/middleware"
)

// DefaultBodyLimit is the request body cap for every route without its own
// (HARDENING-PLAN R-5): a JSON body over it is a 413 before the handler reads
// it (by Content-Length, else while reading).
const DefaultBodyLimit = 1 << 20

// BodyLimit caps request bodies at def bytes, except routes listed in
// overrides ("METHOD /route/template" → bytes; 0 = no cap here, for routes
// whose handler already reads through its own LimitReader).
func BodyLimit(def int64, overrides map[string]int64) echo.MiddlewareFunc {
	limiters := map[int64]echo.MiddlewareFunc{}
	limiter := func(n int64) echo.MiddlewareFunc {
		if mw, ok := limiters[n]; ok {
			return mw
		}
		mw := echomw.BodyLimit(n)
		limiters[n] = mw
		return mw
	}
	limiter(def)
	for _, n := range overrides {
		if n > 0 {
			limiter(n)
		}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		chains := map[int64]echo.HandlerFunc{}
		for n, mw := range limiters {
			chains[n] = mw(next)
		}
		return func(c *echo.Context) error {
			n := def
			if v, ok := overrides[c.Request().Method+" "+c.Path()]; ok {
				n = v
			}
			if n <= 0 {
				return next(c)
			}
			return chains[n](c)
		}
	}
}
