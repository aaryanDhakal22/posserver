package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	authAppSvc "quiccpos/main/internal/app/auth"
	orderSvc "quiccpos/main/internal/app/order"
	"quiccpos/main/internal/infra/database/models"
	"quiccpos/main/internal/infra/database/repositories"
	"quiccpos/main/internal/infra/scheduler"
	sseBroker "quiccpos/main/internal/infra/sse"
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
	tmpLogger := logger.TempLogger()
	tmpLogger.Info().Msg("\n\n ==== Starting API ==== \n\n")

	cfgLogger := tmpLogger.With().Str("module", "config").Logger()
	cfg := config.NewConfig(&cfgLogger)

	lgr := logger.NewLogger(cfg.LogConfig.Level, nil, cfg.LogConfig.Style)
	mainLogger := lgr.With().Str("module", "main").Logger()

	// Shared cancellable context — drives the HTTP server, SQS consumer, and SSE broker.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		cfg.DatabaseConfig.Host,
		cfg.DatabaseConfig.Port,
		cfg.DatabaseConfig.User,
		cfg.DatabaseConfig.Password,
		cfg.DatabaseConfig.Name,
	)

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

	sqsConsumer := sqsconsumer.NewConsumer(sqsClient, cfg.SQSConfig.QueueURL, svc, lgr)
	go sqsConsumer.Start(ctx)

	e := echo.New()
	e.GET("/health", func(c *echo.Context) error {
		return c.String(http.StatusOK, "OK")
	})
	tLogger := lgr.With().Str("module", "transport").Logger()
	transport.AddDefaultMiddlewares(e, &tLogger)
	transport.AddRoutes(e, svc, authService, broker, cfg.AppConfig.AdminPasscode, &tLogger)

	serverLogger := lgr.With().Str("module", "server").Logger()
	transport.StartServer(ctx, e, cfg, &serverLogger)
}
