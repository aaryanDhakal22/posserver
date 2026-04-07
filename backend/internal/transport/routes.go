package transport

import (
	orderSvc "quiccpos/main/internal/app/order"
	"quiccpos/main/internal/transport/handler"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

func AddRoutes(e *echo.Echo, svc *orderSvc.Service, logger *zerolog.Logger) {
	logger.Info().Msg("Adding routes")

	orderHandler := handler.NewOrderHandler(svc, *logger)
	orders := e.Group("/api/v1/orders")
	orders.POST("", orderHandler.Create)
	orders.GET("", orderHandler.GetOrders)
	orders.GET("/latest", orderHandler.GetLatest)
	orders.GET("/:id", orderHandler.GetByID)
}
