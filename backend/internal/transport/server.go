package transport

import (
	"context"
	"net/http"
	"time"

	"quiccpos/main/internal/observability"
	"quiccpos/main/internal/shared/config"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"

	appMiddleware "quiccpos/main/internal/transport/middleware"
)

// streams ends the SSE streams when the server shuts down (F-15): the
// middleware is installed by AddDefaultMiddlewares, Close is registered with
// the http.Server in StartServer.
var streams = appMiddleware.NewDrain()

// BodyLimits are the routes allowed a bigger body than the 1 MB default
// (R-5): "METHOD /route/template" → bytes, 0 = the handler caps it itself
// (its own LimitReader, with its own error text). Keep a route here when it
// takes files or documents; routes_bodylimit_test.go checks each still exists.
var BodyLimits = map[string]int64{
	"POST /admin/v1/app-releases":                   0, // APK, ≤ 100 MB (maxAppAPK)
	"POST /admin/v1/locations/:id/customers/import": 0, // ≤ 32 MB (maxImportBody)
	"POST /store/v1/agent/backup":                   0, // the Pi's SQLite copy, ≤ 64 MB (agentsync.MaxBackup)
	"POST /store/v1/agent/rpc":                      8 << 20,
	"POST /store/v1/me/menu/import":                 0, // CSV, ≤ 2 MB (maxMenuCSV)
	"POST /store/v1/me/menu/bank/apply":             0, // bank document, ≤ 8 MB (maxBankDoc)
	"PUT /store/v1/me/menu/views/:id/layout":        0, // counter layout, ≤ 8 MB (maxLayout)
	"POST /store/v1/me/site/images":                 0, // logo / cover, ≤ 2 MB (site.MaxImageBytes)
}

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
			configureServer(s)
			return nil
		},
	}

	logger.Info().Msg("Starting server")

	if err := sc.Start(ctx, e); err != nil {
		logger.Error().Err(err).Msg("Server stopped")
	}
	logger.Info().Msg("Server drained")
}

// configureServer sets main's timeouts and the shutdown hook that ends the
// SSE streams so Shutdown can drain (F-15).
func configureServer(s *http.Server) {
	s.ReadHeaderTimeout = 10 * time.Second
	s.ReadTimeout = 0
	s.WriteTimeout = 0
	// Idle keep-alive connections are closed after 2 min (R-7).
	s.IdleTimeout = 120 * time.Second
	s.RegisterOnShutdown(streams.Close)
}

// AddDefaultMiddlewares installs OTEL, request logging, recover, the stream
// drain and the body limit, in that order: the request logger reads the span
// the OTEL middleware attached, and Recover runs inside the request logger so
// a panic is logged on the request's logger (with trace_id) and the request
// line still shows the 500.
func AddDefaultMiddlewares(e *echo.Echo, serviceName string, logger *zerolog.Logger) {
	logger.Info().Msg("Adding default middlewares")
	e.Use(observability.EchoOTELMiddleware(serviceName))
	e.Use(appMiddleware.RequestLogger(logger))
	e.Use(appMiddleware.Recover(logger))
	e.Use(streams.Middleware())
	e.Use(appMiddleware.BodyLimit(appMiddleware.DefaultBodyLimit, BodyLimits))
}
