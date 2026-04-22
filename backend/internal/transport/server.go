package transport

import (
	"context"
	"net/http"
	"time"

	"quiccpos/main/internal/shared/config"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/rs/zerolog"

	appMiddleware "quiccpos/main/internal/transport/middleware"
)

// StartServer runs the Echo server until ctx is cancelled (e.g. SIGINT/SIGTERM).
// The caller is responsible for creating the context with signal.NotifyContext so
// the same cancellation can be shared with other long-running goroutines (e.g. SQS consumer).
func StartServer(ctx context.Context, e *echo.Echo, cfg *config.Config, logger *zerolog.Logger) {
	logger.Debug().Msg("Server config: " + cfg.ServerConfig.Host + ":" + cfg.ServerConfig.Port)
	sc := echo.StartConfig{
		Address:         cfg.ServerConfig.Host + ":" + cfg.ServerConfig.Port,
		HideBanner:      true,
		HidePort:        true,
		GracefulTimeout: 10 * time.Second,
		// Echo v5 hardcodes WriteTimeout: 30s which kills long-lived SSE connections.
		// Override: no write timeout (SSE streams indefinitely), but keep a strict
		// ReadHeaderTimeout to protect against slowloris on normal requests.
		BeforeServeFunc: func(s *http.Server) error {
			s.ReadHeaderTimeout = 10 * time.Second
			s.ReadTimeout = 0
			s.WriteTimeout = 0
			return nil
		},
	}

	logger.Info().Msg("Starting server")

	if err := sc.Start(ctx, e); err != nil {
		logger.Error().Err(err).Msg("Server stopped")
	}
}

func AddDefaultMiddlewares(e *echo.Echo, logger *zerolog.Logger) {
	logger.Info().Msg("Adding default middlewares")
	e.Use(appMiddleware.RequestLogger(logger))
	e.Use(middleware.Recover())
}
