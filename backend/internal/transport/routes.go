package transport

import (
	authAppSvc "quiccpos/main/internal/app/auth"
	orderSvc "quiccpos/main/internal/app/order"
	"quiccpos/main/internal/transport/handler"
	appMiddleware "quiccpos/main/internal/transport/middleware"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

type SSEBroker interface {
	Subscribe() (chan []byte, func())
}

func AddRoutes(e *echo.Echo, svc *orderSvc.Service, authSvc *authAppSvc.Service, broker SSEBroker, adminPasscode string, logger *zerolog.Logger) {
	logger.Info().Msg("Adding routes")

	authHandler := handler.NewAuthHandler(authSvc, adminPasscode, *logger)
	e.POST("/api/v1/auth/key", authHandler.SetKey)

	sseHandler := handler.NewSSEHandler(broker, *logger)

	orderHandler := handler.NewOrderHandler(svc, *logger)
	orders := e.Group("/api/v1/orders")
	orders.Use(appMiddleware.APIKeyAuth(authSvc))
	orders.POST("", orderHandler.Create)
	orders.GET("", orderHandler.GetOrders)
	orders.GET("/latest", orderHandler.GetLatest)
	orders.GET("/:id", orderHandler.GetByID)

	events := e.Group("/api/v1/events")
	events.Use(appMiddleware.APIKeyAuth(authSvc))
	events.GET("/orders", sseHandler.StreamOrders)
}
