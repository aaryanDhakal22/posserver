package transport

import "github.com/labstack/echo/v5"

func AddRoutes(e *echo.Echo) {
	e.GET("/", func(c *echo.Context) error {
		return c.String(200, "Hello, World!")
	})
}
