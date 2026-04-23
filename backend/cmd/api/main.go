package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	authAppSvc "quiccpos/main/internal/app/auth"
	orderSvc "quiccpos/main/internal/app/order"
	"quiccpos/main/internal/infra/database/models"
	"quiccpos/main/internal/infra/database/repositories"
	"quiccpos/main/internal/infra/scheduler"
	sqsconsumer "quiccpos/main/internal/infra/sqs"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/migrate"
	"quiccpos/main/internal/observability"
	"quiccpos/main/internal/shared/config"
	"quiccpos/main/internal/shared/logger"
	"quiccpos/main/internal/transport"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
)

// version is set via -ldflags at build time ("-X main.version=<sha>").
// Defaults to "dev" for plain `go run`.
var version = "dev"

func main() {
	tmpLogger := logger.TempLogger()
	tmpLogger.Info().Msg("\n\n ==== Starting API ==== \n\n")

	cfgLogger := tmpLogger.With().Str("module", "config").Logger()
	cfg := config.NewConfig(&cfgLogger)

	// Shared cancellable context — drives the HTTP server, SQS consumer, SSE broker,
	// and the OTEL batch processors.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// --- Observability: must come before the logger is wired so the OTLP
	// log bridge picks up the global LoggerProvider. ------------------------
	shutdownOtel, err := observability.Setup(ctx, observability.Config{
		Endpoint:    cfg.OTELConfig.Endpoint,
		ServiceName: cfg.OTELConfig.ServiceName,
		AppEnv:      cfg.AppConfig.AppEnv,
		Version:     version,
	})
	if err != nil {
		tmpLogger.Error().Err(err).Msg("OTEL setup failed — continuing with no telemetry")
		shutdownOtel = func(context.Context) error { return nil }
	}
	defer func() {
		if err := shutdownOtel(context.Background()); err != nil {
			tmpLogger.Warn().Err(err).Msg("OTEL shutdown reported errors")
		}
	}()

	meters, err := observability.NewMeters()
	if err != nil {
		tmpLogger.Fatal().Err(err).Msg("Failed to create metric instruments")
	}

	lgr := logger.NewLogger(cfg.LogConfig.Level, cfg.LogConfig.Output, cfg.LogConfig.Style, cfg.OTELConfig.ServiceName)
	mainLogger := lgr.With().Str("module", "main").Logger()

	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		cfg.DatabaseConfig.Host,
		cfg.DatabaseConfig.Port,
		cfg.DatabaseConfig.User,
		cfg.DatabaseConfig.Password,
		cfg.DatabaseConfig.Name,
	)

	mainLogger.Info().Ctx(ctx).Msg("Running database migrations")
	if err := migrate.Run(ctx, dsn); err != nil {
		mainLogger.Fatal().Err(err).Msg("Failed to run database migrations")
	}
	mainLogger.Info().Ctx(ctx).Msg("Database migrations complete")

	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		mainLogger.Fatal().Err(err).Msg("Failed to parse DB config")
	}
	// otelpgx spans every query, records rows-affected, and marks failed
	// statements as Error. No call-site changes needed; it hooks into pgx.
	poolConfig.ConnConfig.Tracer = otelpgx.NewTracer()

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		mainLogger.Fatal().Err(err).Msg("Failed to connect to database")
	}
	defer pool.Close()
	mainLogger.Info().Ctx(ctx).Msg("Connected to database")

	// SSE broker — fans out new orders to connected agents.
	broker := sseBroker.New()

	// Dependency injection
	repo := repositories.NewOrderRepository(pool, lgr)
	svc := orderSvc.NewOrderService(repo, broker, lgr)

	authRepo := repositories.NewAuthKeyRepository(pool, lgr)
	authService := authAppSvc.NewAuthKeyService(authRepo, lgr)

	sqsClient, err := sqsconsumer.NewSQSClient(ctx, cfg, lgr)
	if err != nil {
		mainLogger.Fatal().Err(err).Msg("Failed to create SQS client")
	}

	scheduler.StartOrderNumberReset(ctx, func(c context.Context) error {
		return models.New(pool).ResetOrderNumber(c)
	}, lgr)

	sqsConsumer := sqsconsumer.NewConsumer(sqsClient, cfg.SQSConfig.QueueURL, svc, lgr, meters)
	go sqsConsumer.Start(ctx)

	e := echo.New()
	e.GET("/health", func(c *echo.Context) error {
		return c.String(http.StatusOK, "OK")
	})
	tLogger := lgr.With().Str("module", "transport").Logger()
	transport.AddDefaultMiddlewares(e, cfg.OTELConfig.ServiceName, &tLogger)
	transport.AddRoutes(e, svc, authService, broker, cfg.AppConfig.AdminPasscode, &tLogger, meters)

	serverLogger := lgr.With().Str("module", "server").Logger()
	transport.StartServer(ctx, e, cfg, &serverLogger)
}
