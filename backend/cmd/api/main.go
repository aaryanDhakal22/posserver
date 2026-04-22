package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	authAppSvc "quiccpos/main/internal/app/auth"
	orderSvc "quiccpos/main/internal/app/order"
	"quiccpos/main/internal/infra/database/repositories"
	sqsconsumer "quiccpos/main/internal/infra/sqs"
	"quiccpos/main/internal/migrate"
	"quiccpos/main/internal/shared/config"
	"quiccpos/main/internal/shared/logger"
	"quiccpos/main/internal/transport"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
)

func main() {
	// Temp logger for bootstrap
	tmpLogger := logger.TempLogger()
	tmpLogger.Info().Msg("\n\n ==== Starting API ==== \n\n")

	// Config
	cfgLogger := tmpLogger.With().Str("module", "config").Logger()
	cfg := config.NewConfig(&cfgLogger)

	// Main logger
	lgr := logger.NewLogger(cfg.LogConfig.Level, nil, cfg.LogConfig.Style)
	mainLogger := lgr.With().Str("module", "main").Logger()

	// Shared cancellable context — drives both the HTTP server and the SQS consumer.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// PostgreSQL DSN
	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		cfg.DatabaseConfig.Host,
		cfg.DatabaseConfig.Port,
		cfg.DatabaseConfig.User,
		cfg.DatabaseConfig.Password,
		cfg.DatabaseConfig.Name,
	)

	// Run database migrations before opening the pool.
	mainLogger.Info().Msg("Running database migrations")
	if err := migrate.Run(ctx, dsn); err != nil {
		mainLogger.Fatal().Err(err).Msg("Failed to run database migrations")
	}
	mainLogger.Info().Msg("Database migrations complete")

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		mainLogger.Fatal().Err(err).Msg("Failed to connect to database")
	}
	defer pool.Close()
	mainLogger.Info().Msg("Connected to database")

	// Dependency injection
	repo := repositories.NewOrderRepository(pool, lgr)
	svc := orderSvc.NewOrderService(repo, lgr)

	authRepo := repositories.NewAuthKeyRepository(pool, lgr)
	authService := authAppSvc.NewAuthKeyService(authRepo, lgr)

	// Setup SQS client
	sqsClient, err := sqsconsumer.NewSQSClient(ctx, cfg, lgr)
	if err != nil {
		mainLogger.Fatal().Err(err).Msg("Failed to create SQS client")
	}

	// Start SQS consumer
	sqsConsumer := sqsconsumer.NewConsumer(sqsClient, cfg.SQSConfig.QueueURL, svc, lgr)
	go sqsConsumer.Start(ctx)

	// Echo HTTP server
	e := echo.New()
	e.GET("/health", func(c *echo.Context) error {
		return c.String(http.StatusOK, "OK")
	})
	tLogger := lgr.With().Str("module", "transport").Logger()
	transport.AddDefaultMiddlewares(e, &tLogger)
	transport.AddRoutes(e, svc, authService, cfg.AppConfig.AdminPasscode, &tLogger)

	serverLogger := lgr.With().Str("module", "server").Logger()
	transport.StartServer(ctx, e, cfg, &serverLogger)
}
