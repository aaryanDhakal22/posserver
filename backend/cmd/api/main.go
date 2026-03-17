package main

import (
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
)

func main() {
	e := echo.New()
	e.Use(middleware.RequestLogger())
	e.GET("/", func(c *echo.Context) error {
		return c.String(200, "Hello, World!")
	})
	if err := e.Start(":1323"); err != nil {
		panic(err)
	}
}
