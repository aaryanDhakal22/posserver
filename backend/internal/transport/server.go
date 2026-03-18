package transport

import (
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
)

func StartServer(e *echo.Echo) {
}

func AddDefaultMiddlewares(e *echo.Echo) {
	e.Use(middleware.RequestLogger())
	e.Use(middleware.Recover())
}
