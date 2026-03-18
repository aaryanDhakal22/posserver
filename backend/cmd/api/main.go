package main

import (
	"quiccpos/main/internal/shared/config"
	"quiccpos/main/internal/shared/logger"
	"quiccpos/main/internal/transport"

	"github.com/labstack/echo/v5"
)

func main() {
	// Create a temp logger
	tmpLogger := logger.TempLogger()

	// Start the main function
	tmpLogger.Info().Msg("\n\n ==== Starting API ==== \n\n")

	// Get the config
	cfgLogger := tmpLogger.With().Str("module", "config").Logger()
	cfg := config.NewConfig(&cfgLogger)

	// Create a default logger
	lgr := logger.NewLogger(cfg.LogConfig.Level, nil, cfg.LogConfig.Style)

	// Create a main module logger
	mainLogger := lgr.With().Str("module", "main").Logger()

	// Create a new Echo instance
	mainLogger.Info().Msg("Creating Echo instance")
	e := echo.New()

	// Add Default middlewares
	transport.AddDefaultMiddlewares(e)

	// Add routes
	transport.AddRoutes(e)

	// Test test
	if err := e.Start(":1323"); err != nil {
		panic(err)
	}
}
