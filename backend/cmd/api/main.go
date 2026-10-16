package main

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"os"
	"os/signal"
	"quiccpos/main/internal/app/agentsync"
	floorApp "quiccpos/main/internal/app/floor"
	"quiccpos/main/internal/app/printerstatus"
	"quiccpos/main/internal/casting/online"
	"quiccpos/main/internal/dispatch"
	"quiccpos/main/internal/dispatch/handlers"
	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/repositories"
	"quiccpos/main/internal/infra/pushover"
	"quiccpos/main/internal/migrate"
	"quiccpos/main/internal/observability"
	"quiccpos/main/internal/rpc"
	"quiccpos/main/internal/shared/config"
	"quiccpos/main/internal/shared/logger"
	"quiccpos/main/internal/transport"
	"quiccpos/main/internal/transport/session"
	"syscall"
	"time"

	authAppSvc "quiccpos/main/internal/app/auth"
	menuApp "quiccpos/main/internal/app/menu"
	operatorApp "quiccpos/main/internal/app/operator"
	orderSvc "quiccpos/main/internal/app/order"
	tenancyApp "quiccpos/main/internal/app/tenancy"

	sqsconsumer "quiccpos/main/internal/infra/sqs"
	sseBroker "quiccpos/main/internal/infra/sse"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// version + commit are injected at build time via -ldflags. CI passes them
// from VERSION + the git sha so the running binary can be inspected via
// the `/version` endpoint and so OTEL resource attributes carry real
// values rather than `dev`.
var (
	version = "dev"
	commit  = "unknown"
)

// storeResolver adapts tenancy.Service's ResolveStore (store_id first, then the
// store_name fallback) to the online caster's StoreResolver interface
// (uuid.UUID → order.TenantID/LocationID).
type storeResolver struct{ svc *tenancyApp.Service }

func (r storeResolver) ResolveStore(ctx context.Context, storeID int64, storeName string) (order.TenantID, order.LocationID, string, error) {
	tid, lid, method, err := r.svc.ResolveStore(ctx, storeID, storeName)
	if err != nil {
		return order.TenantID{}, order.LocationID{}, string(method), err
	}
	return order.TenantID(tid), order.LocationID(lid), string(method), nil
}

func main() {
	tmpLogger := logger.TempLogger()
	tmpLogger.Info().Msg("\n\n ==== Starting API ==== \n\n")

	cfgLogger := tmpLogger.With().Str("module", "config").Logger()
	cfg := config.NewConfig(&cfgLogger)

	// Shared cancellable context — drives the HTTP server, SQS consumer,
	// dispatcher, and OTEL batch processors.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

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
	poolConfig.ConnConfig.Tracer = otelpgx.NewTracer()

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		mainLogger.Fatal().Err(err).Msg("Failed to connect to database")
	}
	defer pool.Close()
	mainLogger.Info().Ctx(ctx).Msg("Connected to database")

	// --- Wiring order matters here:
	// 1. Build the SSE broker (no waker yet).
	// 2. Build the SSE handler that adapts the broker to the dispatch.Handler interface.
	// 3. Build the order repo, passing in the handler names so it inserts
	//    one outbox_dispatches row per handler in the same tx as Create.
	// 4. Build the dispatcher and tell the broker to wake it on Subscribe.
	// 5. Build the order service with the dispatcher as its Waker.
	// ----------------------------------------------------------------------

	broker := sseBroker.New(lgr, meters)

	// Duplex RPC core: main→agent commands ride the broker's SSE; agent→main
	// replies/reports land on POST /store/v1/agent/rpc.
	requester := rpc.NewRequester(broker, meters, lgr)

	// Live printer roster the agent reports over the duplex channel (advisory).
	printerCache := printerstatus.NewCache()
	requester.RegisterInbound(rpc.MsgPrinterReport, printerCache.Handle)

	// SSE handler reads the snapshot directly from the outbox payload —
	// no aggregate loader needed. The order repo is built below with the
	// handler names so per-handler dispatch rows are written in the order
	// transaction.
	sseHandler := handlers.NewSSE(broker, lgr)

	allHandlers := []dispatch.Handler{sseHandler}
	handlerNames := make([]string, 0, len(allHandlers))
	for _, h := range allHandlers {
		handlerNames = append(handlerNames, h.Name())
	}

	repo := repositories.NewOrderRepository(pool, lgr, handlerNames)
	outboxRepo := repositories.NewOutboxRepository(pool, lgr)

	hooks := dispatch.Hooks{
		OnEventDispatched: func(handler string, ev order.EventType) {
			meters.OutboxDispatched.Add(ctx, 1, metric.WithAttributes(
				attribute.String("handler", handler),
				attribute.String("event_type", string(ev)),
			))
		},
		OnEventFailed: func(handler string, ev order.EventType, transient bool) {
			meters.OutboxFailed.Add(ctx, 1, metric.WithAttributes(
				attribute.String("handler", handler),
				attribute.String("event_type", string(ev)),
				attribute.Bool("transient", transient),
			))
		},
	}
	dispatcher := dispatch.New(outboxRepo, allHandlers, lgr, dispatch.WithHooks(hooks))
	broker.SetWaker(dispatcher)
	go dispatcher.Run(ctx)

	if err := observability.RegisterPendingDispatch(meters, outboxRepo, handlerNames); err != nil {
		mainLogger.Warn().Err(err).Msg("Failed to register outbox.events.pending gauge")
	}

	svc := orderSvc.NewOrderService(repo, dispatcher, lgr)

	// Floor read model: the agent pushes its floor events (accept / ready /
	// on delivery / delivered / driver roster) as `floor.events` reports.
	floorService := floorApp.NewService(repo, repo, lgr, meters)
	requester.RegisterInbound(floorApp.MsgFloorEvents, floorService.Handle)

	authRepo := repositories.NewDeviceAuthRepository(pool, lgr)
	authService := authAppSvc.NewService(authRepo, lgr)

	tenancyRepo := repositories.NewTenancyRepository(pool, lgr)
	tenancyService := tenancyApp.NewService(tenancyRepo, lgr)
	// A revoked / deleted device's live SSE stream is cut at once (a revoked
	// agent must not keep printing until its next request fails).
	tenancyService.OnDeviceGone(func(id uuid.UUID) { broker.DisconnectDevice(id) })

	menuRepo := repositories.NewMenuRepository(pool, lgr)
	menuService := menuApp.NewService(menuRepo, tenancyService, meters, lgr)

	// Agent reliability + headless recovery: order acks + re-sends, the
	// agent's state/health reports, link with the manager login, restore,
	// backups, and the opening-hours offline pager. Runtime secrets handed to
	// a linked agent come from AGENT_SECRET_* (read here, never logged).
	agentSecrets := agentsync.Secrets{
		PushoverAppToken: os.Getenv("AGENT_SECRET_PUSHOVER_APP_TOKEN"),
		PushoverUserKey:  os.Getenv("AGENT_SECRET_PUSHOVER_USER_KEY"),
		PushoverBossKey:  os.Getenv("AGENT_SECRET_PUSHOVER_BOSS_KEY"),
		OTLPEndpoint:     os.Getenv("AGENT_SECRET_OTLP_ENDPOINT"),
		OTLPHeaders:      os.Getenv("AGENT_SECRET_OTLP_HEADERS"),
		TailscaleAuthKey: os.Getenv("AGENT_SECRET_TAILSCALE_AUTHKEY"),
	}
	pager := pushover.New(agentSecrets.PushoverAppToken, agentSecrets.PushoverUserKey, agentSecrets.PushoverBossKey)
	agentSync := agentsync.New(pool, repo, repo, broker, tenancyService, agentSecrets, pager, lgr)
	if v, err := time.ParseDuration(os.Getenv("ORDER_REDELIVER_AFTER")); err == nil && v > 0 {
		agentSync.RedeliverAfter = v
	}
	requester.RegisterInbound(agentsync.MsgOrderAck, agentSync.HandleAck)
	requester.RegisterInbound(agentsync.MsgAgentState, agentSync.HandleState)
	requester.RegisterInbound(agentsync.MsgAgentHealth, agentSync.HandleHealth)
	go agentSync.RunRedeliver(ctx)
	go agentSync.RunOfflinePager(ctx)

	operatorRepo := repositories.NewOperatorRepository(pool, lgr)
	operatorService := operatorApp.NewService(operatorRepo, tenancyService, lgr)

	sqsClient, err := sqsconsumer.NewSQSClient(ctx, cfg, lgr)
	if err != nil {
		mainLogger.Fatal().Err(err).Msg("Failed to create SQS client")
	}

	// Per-source caster registry. Each entry maps a `source`
	// MessageAttribute value to its normalisation strategy. Adding a source
	// (mobile / pos / kiosk) is one new caster + one entry here. The online
	// caster routes each order to a tenant/location by its store_name (see
	// storeResolver + tenancy.ResolveStoreByName).
	onlineCaster := online.New(lgr, storeResolver{svc: tenancyService}, menuService)
	casters := map[order.Source]sqsconsumer.Caster{
		order.SourceOnline: onlineCaster,
	}

	sqsConsumer := sqsconsumer.NewConsumer(sqsClient, cfg.SQSConfig.QueueURL, casters, svc, lgr, meters)
	go sqsConsumer.Start(ctx)

	// Store-manager session signer. Secret is stable (Doppler-managed) so
	// sessions survive a blue-green swap; Secure cookies only outside dev.
	sessionSigner, err := session.New(cfg.AppConfig.SessionSecret, cfg.AppConfig.AppEnv != "dev")
	if err != nil {
		lgr.Fatal().Err(err).Msg("failed to build session signer")
	}

	e := echo.New()
	tLogger := lgr.With().Str("module", "transport").Logger()
	transport.AddDefaultMiddlewares(e, cfg.OTELConfig.ServiceName, &tLogger)
	transport.AddRoutes(e, transport.RouterDeps{
		Service:         svc,
		AuthService:     authService,
		TenancyService:  tenancyService,
		Broker:          broker,
		Publisher:       broker,
		AdminAPIKey:     cfg.AppConfig.AdminAPIKey,
		Version:         version,
		Commit:          commit,
		Pool:            pool,
		SQS:             sqsClient,
		QueueURL:        cfg.SQSConfig.QueueURL,
		Meters:          meters,
		Logger:          tLogger,
		SessionSigner:   sessionSigner,
		Requester:       requester,
		PrinterCache:    printerCache,
		MenuService:     menuService,
		FloorService:    floorService,
		OperatorService: operatorService,
		AgentSync:       agentSync,
	})

	serverLogger := lgr.With().Str("module", "server").Logger()
	transport.StartServer(ctx, e, cfg, &serverLogger)
}
