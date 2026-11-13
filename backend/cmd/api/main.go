package main

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"os"
	"os/signal"
	"quiccpos/main/internal/app/agentsync"
	daycloseApp "quiccpos/main/internal/app/dayclose"
	diagApp "quiccpos/main/internal/app/diag"
	floorApp "quiccpos/main/internal/app/floor"
	geoApp "quiccpos/main/internal/app/geo"
	"quiccpos/main/internal/app/printerstatus"
	"quiccpos/main/internal/casting/online"
	"quiccpos/main/internal/dispatch"
	"quiccpos/main/internal/dispatch/handlers"
	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/auditconn"
	"quiccpos/main/internal/infra/database/repositories"
	"quiccpos/main/internal/infra/pushover"
	"quiccpos/main/internal/migrate"
	"quiccpos/main/internal/observability"
	"quiccpos/main/internal/rpc"
	"quiccpos/main/internal/shared/config"
	"quiccpos/main/internal/shared/dbpool"
	"quiccpos/main/internal/shared/logger"
	"quiccpos/main/internal/shared/safego"
	"quiccpos/main/internal/transport"
	"quiccpos/main/internal/transport/clientip"
	"quiccpos/main/internal/transport/handler"
	appMiddleware "quiccpos/main/internal/transport/middleware"
	"quiccpos/main/internal/transport/session"
	"strings"
	"syscall"
	"time"

	authAppSvc "quiccpos/main/internal/app/auth"
	"quiccpos/main/internal/app/bankimport"
	callcenterApp "quiccpos/main/internal/app/callcenter"
	cardholdApp "quiccpos/main/internal/app/cardhold"
	channelApp "quiccpos/main/internal/app/channel"
	"quiccpos/main/internal/app/customers"
	familiesApp "quiccpos/main/internal/app/families"
	"quiccpos/main/internal/app/kitchen"
	menuApp "quiccpos/main/internal/app/menu"
	menuviewApp "quiccpos/main/internal/app/menuview"
	"quiccpos/main/internal/app/onlinectl"
	operatorApp "quiccpos/main/internal/app/operator"
	"quiccpos/main/internal/app/optionflags"
	orderSvc "quiccpos/main/internal/app/order"
	"quiccpos/main/internal/app/orderport"
	pricingApp "quiccpos/main/internal/app/pricing"
	"quiccpos/main/internal/app/receipts"
	siteApp "quiccpos/main/internal/app/site"
	"quiccpos/main/internal/app/squarepay"
	staffApp "quiccpos/main/internal/app/staff"
	storestatusApp "quiccpos/main/internal/app/storestatus"
	tenancyApp "quiccpos/main/internal/app/tenancy"
	"quiccpos/main/internal/app/tracking"
	"quiccpos/main/internal/domain/notify"
	"quiccpos/main/internal/domain/payment"
	tenancyDomain "quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/infra/square"

	sqsconsumer "quiccpos/main/internal/infra/sqs"
	sseBroker "quiccpos/main/internal/infra/sse"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel/attribute"
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
	// `app migrate [status]`: the deploy's one-off migration container
	// (internal/migrate/command.go, deploy/main-deploy.sh); nothing else starts.
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		os.Exit(migrate.Command(os.Args[2:]))
	}
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
	if err := observability.RegisterInfo(version, commit); err != nil {
		tmpLogger.Warn().Err(err).Msg("Failed to register main.info gauge")
	}

	lgr := logger.NewLogger(cfg.LogConfig.Level, cfg.LogConfig.Output, cfg.LogConfig.Style, cfg.OTELConfig.ServiceName)
	mainLogger := lgr.With().Str("module", "main").Logger()
	// Every goroutine below runs through safego: a panic is logged with its
	// stack and counted (main.panics), and a loop starts again (F-2).
	safego.SetLogger(lgr)

	// PPROF_ADDR (off by default): net/http/pprof on its own loopback-only
	// server, reached over SSH (R-1). A non-loopback address is refused.
	if _, err := observability.StartPprof(ctx, os.Getenv("PPROF_ADDR"), mainLogger); err != nil {
		mainLogger.Error().Err(err).Msg("pprof not started")
	}

	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		dsnValue(cfg.DatabaseConfig.Host),
		dsnValue(cfg.DatabaseConfig.Port),
		dsnValue(cfg.DatabaseConfig.User),
		dsnValue(cfg.DatabaseConfig.Password),
		dsnValue(cfg.DatabaseConfig.Name),
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
	// R-4: pool cap + statement / idle-in-transaction timeouts (DB_MAX_CONNS,
	// DB_STATEMENT_TIMEOUT, DB_IDLE_TX_TIMEOUT).
	if err := dbpool.Apply(poolConfig, os.Getenv); err != nil {
		mainLogger.Fatal().Err(err).Msg("Bad database pool setting")
	}
	// The audit triggers record who acted: each connection handed to a
	// request carries its actor + trace id (baseline 2026-11-12).
	auditconn.Install(poolConfig, appMiddleware.AuditActor)

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		mainLogger.Fatal().Err(err).Msg("Failed to connect to database")
	}
	defer pool.Close()
	mainLogger.Info().Ctx(ctx).Msg("Connected to database")

	// Metric store labels (tenant / location slug), cached per store.
	observability.SetStoreLookup(repositories.StoreSlugs(pool))

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

	// Channel tracking (3.0): a keyed channel's order changes feed its live
	// stream. Wired to the order service + the owner pager further down.
	trackingService := tracking.NewService(repositories.NewTrackingRepository(pool), lgr)

	allHandlers := []dispatch.Handler{sseHandler, handlers.NewTracking(trackingService)}
	handlerNames := make([]string, 0, len(allHandlers))
	for _, h := range allHandlers {
		handlerNames = append(handlerNames, h.Name())
	}

	repo := repositories.NewOrderRepository(pool, lgr, handlerNames)
	outboxRepo := repositories.NewOutboxRepository(pool, lgr)

	hooks := dispatch.Hooks{
		OnEventDispatched: func(ctx context.Context, handler string, ev order.DomainEvent, tenantID uuid.UUID) {
			meters.OutboxDispatched.Add(ctx, 1, observability.WithStore(ctx, tenantID,
				attribute.String("handler", handler),
				attribute.String("event_type", string(ev.Type)),
			))
			// Only the order frame is a hop: the SSE handler skips every other
			// event type. OccurredAt = when the order was received + persisted.
			if handler == handlers.HandlerNameSSE && ev.Type == order.EventOrderReceived {
				observability.RecordLatency(ctx, meters.OrdersDispatchLatency, ev.OccurredAt, time.Now(),
					observability.WithStore(ctx, tenantID))
			}
		},
		OnEventFailed: func(ctx context.Context, handler string, ev order.EventType, tenantID uuid.UUID, transient bool) {
			meters.OutboxFailed.Add(ctx, 1, observability.WithStore(ctx, tenantID,
				attribute.String("handler", handler),
				attribute.String("event_type", string(ev)),
				attribute.Bool("transient", transient),
			))
		},
	}
	dispatcher := dispatch.New(outboxRepo, allHandlers, lgr, dispatch.WithHooks(hooks))
	broker.SetWaker(dispatcher)
	safego.Loop(ctx, "outbox.dispatcher", dispatcher.Run)

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
	// The menu bank's views (3.0); the vendor view's name mappings feed kitchen routing.
	menuBankRepo := repositories.NewMenuBankRepository(pool)
	menuViewService := menuviewApp.NewService(menuBankRepo, menuService, lgr)
	bankImportService := bankimport.NewService(repositories.NewBankImportRepository(pool), lgr)
	menuService.SetVendorMapper(menuViewService)
	menuService.SetFamilies(repositories.NewFamilyRepository(pool))

	// Agent reliability + headless recovery: order acks + re-sends, the
	// agent's state/health reports, link with the manager login, restore,
	// backups, and the opening-hours offline pager. Runtime secrets handed to
	// a linked agent come from AGENT_SECRET_* (read here, never logged).
	agentSecrets := agentsync.Secrets{
		PushoverAppToken: os.Getenv("AGENT_SECRET_PUSHOVER_APP_TOKEN"),
		PushoverUserKey:  os.Getenv("AGENT_SECRET_PUSHOVER_USER_KEY"),
		PushoverBossKey:  os.Getenv("AGENT_SECRET_PUSHOVER_BOSS_KEY"),
		// The admin's Pushover device names (comma list): system events go only there.
		PushoverAdminDevices: os.Getenv("AGENT_SECRET_PUSHOVER_ADMIN_DEVICE"),
		OTLPEndpoint:         os.Getenv("AGENT_SECRET_OTLP_ENDPOINT"),
		OTLPHeaders:          os.Getenv("AGENT_SECRET_OTLP_HEADERS"),
		TailscaleAuthKey:     os.Getenv("AGENT_SECRET_TAILSCALE_AUTHKEY"),
	}
	// PUSHOVER_API_URL points main's Pushover at a test fake (default: the real API).
	pushoverURL := os.Getenv("PUSHOVER_API_URL")
	pager := pushover.New(agentSecrets.PushoverAppToken, agentSecrets.PushoverUserKey, agentSecrets.PushoverBossKey)
	pager.SetEndpoint(pushoverURL)
	// The admin's phones only (drivers + the store owner share the account).
	adminDevices := tenancyDomain.SplitDevices(agentSecrets.PushoverAdminDevices)
	tenancyService.SetReservedDevices(adminDevices) // no store's manager may save one of ours as an owner phone
	pager.SetDevices(adminDevices)
	agentSync := agentsync.New(pool, repo, repo, broker, tenancyService, agentSecrets, pager, lgr)
	if v, err := time.ParseDuration(os.Getenv("ORDER_REDELIVER_AFTER")); err == nil && v > 0 {
		agentSync.RedeliverAfter = v
	}
	requester.RegisterInbound(agentsync.MsgOrderAck, agentSync.HandleAck)
	requester.RegisterInbound(agentsync.MsgAgentState, agentSync.HandleState)
	requester.RegisterInbound(agentsync.MsgAgentHealth, agentSync.HandleHealth)
	// Pi updates + printer auto-follow (2.0.6): update status reports, a
	// printer the Pi followed to a new IP, a found printer added from the Pi.
	// A printer change pushes the fresh store_config to the agent, the same
	// way a manager edit in /app/store does.
	requester.RegisterInbound(rpc.MsgAgentUpdateStatus, agentSync.HandleUpdateStatus)
	requester.RegisterInbound(rpc.MsgPrinterMoved, agentSync.HandlePrinterMoved)
	requester.RegisterInbound(rpc.MsgPrinterAdd, agentSync.HandlePrinterAdd)
	requester.RegisterInbound(rpc.MsgPrinterWidth, agentSync.HandlePrinterWidth)
	storeConfigPub := handler.NewStoreConfigPublisher(broker, tenancyService, lgr)
	// main pages the store owner when a Pi goes offline (4.0.2): store_config
	// says so and the Pi skips its own owner offline alert.
	storeConfigPub.SetOwnerOfflineByMain(agentSync.OwnerOfflineAlertByMain)
	agentSync.OnPrintersChanged(storeConfigPub.Publish)
	safego.Loop(ctx, "agentsync.redeliver", agentSync.RunRedeliver)
	safego.Loop(ctx, "agentsync.offline_pager", agentSync.RunOfflinePager)

	// Tablet diagnostics: reports arrive via the Pi (RPC diag.report) or
	// straight from the tablet (report key). Pages go to the OWNER only — a
	// separate pager without the boss key.
	// The store owner's alert test (Store › Details) sends through it too.
	ownerPager := pushover.New(agentSecrets.PushoverAppToken, agentSecrets.PushoverUserKey, "")
	ownerPager.SetEndpoint(pushoverURL)
	ownerPager.SetDevices(adminDevices)
	diagService := diagApp.New(pool, ownerPager, tenancyService, lgr)

	// The notification port (3.0): problems with channel orders → the log and
	// the store owner's phones (+ the admin's).
	var devNotices *notify.Recorder // dev: tests read the owner notices
	if cfg.AppConfig.AppEnv == "dev" {
		devNotices = &notify.Recorder{}
	}
	// Test stores (the flows' and pytest's throwaway stores) never page the
	// owner's phones unless NOTIFY_TEST_STORES says so: log (default) |
	// pushover | device:<name> (that one device of the owner's account).
	testMode, ok := notify.ParseTestStoresMode(os.Getenv("NOTIFY_TEST_STORES"))
	if !ok {
		lgr.Warn().Str("var", "NOTIFY_TEST_STORES").Msg("unknown mode: test stores' notices are logged only")
	}
	var testDevice notify.Pager
	if d := notify.DeviceOf(testMode); d != "" {
		p := pushover.New(agentSecrets.PushoverAppToken, agentSecrets.PushoverUserKey, "")
		p.SetEndpoint(pushoverURL)
		p.SetDevices([]string{d})
		testDevice = p
	}
	ownerNotices := notify.Pushover{Pager: ownerPager, Devices: func(ctx context.Context, locationID uuid.UUID) ([]string, error) {
		l, err := tenancyService.GetLocation(ctx, locationID)
		if err != nil {
			return nil, err
		}
		return tenancyDomain.OwnerDevices(l.Config), nil
	}}
	notifier := notify.Fanout{notify.Log{Logger: lgr}, notify.TestStores{Inner: ownerNotices, Mode: testMode, Device: testDevice,
		IsTest: func(ctx context.Context, locationID uuid.UUID) (bool, error) {
			l, err := tenancyService.GetLocation(ctx, locationID)
			if err != nil {
				return false, err
			}
			t, err := tenancyService.GetTenant(ctx, l.TenantID)
			if err != nil {
				return false, err
			}
			return t.TestStore, nil
		}}}
	if devNotices != nil {
		notifier = append(notifier, devNotices)
	}
	trackingService.Wire(svc, tenancyService, notifier)
	safego.Loop(ctx, "tracking.maintenance", trackingService.RunMaintenance)
	requester.RegisterInbound(diagApp.MsgDiagReport, diagService.HandleRPC)
	safego.Loop(ctx, "diag.prune", diagService.RunPrune)

	// Delivery maps (2.0.9): delivery orders are geocoded + routed in the
	// background after ingest and pushed to the agent as RPC order.geo; a
	// (re)connecting agent gets what it has not acked. The agent asks back
	// with geo.pin (a staff pin) and geo.trip (best stop order).
	geoCfg := geoApp.ConfigFromEnv()
	geoService := geoApp.New(pool, tenancyService, requester, geoCfg, lgr)
	geoService.OnStoreChanged(storeConfigPub.Publish)
	svc.OnCreated(geoService.OnOrderCreated)
	broker.OnSubscribe(geoService.WakeLocation)
	requester.RegisterInbound(rpc.MsgGeoPin, geoService.HandlePin)
	requester.RegisterInbound(rpc.MsgGeoTrip, geoService.HandleTrip)
	// 2.1.0: address candidates (geo.choose). geo.locate / geo.forget (the
	// SoftTouch POS stops) are retired: unknown type, 202 no-op.
	requester.RegisterInbound(rpc.MsgGeoChoose, geoService.HandleChoose)
	requester.RegisterInbound(rpc.MsgGeoSearch, geoService.HandleSearch)
	// 2.2.0: the tablet sets the delivery radius through the Pi.
	requester.RegisterInbound(rpc.MsgLocationRadius, geoService.HandleRadius)
	floorService.OnDeliveryRun(repositories.NewDeliveryRunStore(pool))
	mainLogger.Info().Str("geo_providers", geoService.Providers().Name()).Msg("geo worker configured")
	safego.Loop(ctx, "geo.worker", geoService.Run)

	// Store status (3.0): ordering settings, pause, hours exceptions, the
	// delivery check, and 86'd items back at each location's reset hour.
	storeStatusService := storestatusApp.NewService(repositories.NewStoreStatusRepository(pool), tenancyService, geoService, lgr)
	safego.Loop(ctx, "storestatus.restock", storeStatusService.RunRestock)
	// The counter terminal (TERMINAL-PLAN 0d): staff + terminal settings, the
	// Pi's counter orders / payments / voids / journal, menu.changed pushes.
	staffService := staffApp.NewService(repositories.NewStaffRepository(pool), broker, lgr)
	floorService.OnCounter(repo, staffService)
	floorService.OnStaffLimits(staffService) // M-7: refunds over a person's limit are flagged
	floorService.SetNotifier(notifier)       // flagged money pages the owner (≤ 1/h per store)
	// Drawers per cashier shift + the Z report (terminal Stage 2): drawer.* / z.report from the Pi.
	dayCloseService := daycloseApp.NewService(repositories.NewDayCloseRepository(pool), repositories.NoRows, lgr)
	dayCloseService.SetMeters(&meters) // CM-14: drawers' over / short, Z differences
	floorService.OnDayClose(dayCloseService)
	floorService.OnCounterPlaced(geoService.OnOrderCreated)
	floorService.OnCounterMoved(geoService.OnOrderMoved) // a delivery's new address is located again
	safego.Loop(ctx, "staff.watch_menus", func(ctx context.Context) { staffService.WatchMenus(ctx, menuBankRepo, broker) })
	// Quibase's online-ordering controls through the Pi (weborder stage 3): pause + sold out.
	onlineCtl := onlinectl.NewService(storeStatusService, repositories.NewStockRepository(pool), lgr)
	requester.RegisterInbound(onlinectl.MsgOnlineStatus, onlineCtl.HandleStatus)
	requester.RegisterInbound(onlinectl.MsgOnlinePause, onlineCtl.HandlePause)
	requester.RegisterInbound(onlinectl.MsgOnlineResume, onlineCtl.HandleResume)
	requester.RegisterInbound(onlinectl.MsgMenuStock, onlineCtl.HandleStock)
	requester.RegisterInbound(onlinectl.MsgMenuStockSet, onlineCtl.HandleStockSet)
	pricingService := pricingApp.NewService(repositories.NewPricingRepository(pool), menuViewService, storeStatusService, tenancyService, lgr)
	// The payment port: cash everywhere; the test card on dev and staging
	// only — no card charging in prod until Square (the owner, 2026-11-02).
	gateways := map[string]payment.Gateway{payment.MethodCash: payment.Cash{}}
	var testCard payment.Gateway
	if env := cfg.AppConfig.AppEnv; env == "dev" || env == "staging" {
		testCard = payment.NewTestCard()
		gateways[payment.MethodCard] = testCard
	}
	// Square (weborder stage 2): a store with Square connected takes card
	// orders as holds, captured at clock-out / Close batch / after 24 h.
	sqCfg, sqFake := squarepay.FromEnv(cfg.AppConfig.AppEnv, lgr)
	squareRepo := repositories.NewSquareRepository(pool)
	squareService, err := squarepay.NewService(sqCfg, square.New(sqCfg.BaseURL, sqCfg.AppID, sqCfg.AppSecret, nil), squareRepo, tenancyService, lgr)
	if err != nil {
		lgr.Fatal().Err(err).Msg("square setup")
	}
	squareService.SetNotifier(notifier)
	// The counter terminal's setup test charge ($1, no order) is refunded
	// through the Pi: RPC square.test_refund (TERMINAL-SETUP-PLAN, owner Q3).
	requester.RegisterInbound(squarepay.MsgSquareTestRefund, squareService.HandleTestRefund)
	// Send to phone (agent contract 2.10z): the Square app on a cashier's phone.
	requester.RegisterInbound(squarepay.MsgPosConfig, squareService.HandlePosConfig)
	requester.RegisterInbound(squarepay.MsgPosLookup, squareService.HandlePosLookup)
	// The call center's remote terminal (CALLCENTER-PLAN.md): device tokens, PIN
	// sessions for role callcenter, the relay to the store's Pi (terminal.relay)
	// and the Pi's terminal events back (terminal.events).
	callcenterService := callcenterApp.NewService(repositories.NewCallcenterRepository(pool), staffService, storeStatusService,
		broker, requester, broker, squareService, tenancyService.GetLocation, lgr)
	broker.OnGone(callcenterService.PiGone) // a relayed call in flight when the Pi's stream drops fails at once
	requester.RegisterInbound(squarepay.MsgPosFakeCharge, squareService.HandlePosFakeCharge)
	// Square on the counter terminal (agent contract §2.10r): the reader's
	// sign-in, a typed card's payment, a refund of either.
	requester.RegisterInbound(squarepay.MsgSquareSession, squareService.HandleSession)
	requester.RegisterInbound(squarepay.MsgSquareKeyedPayment, squareService.HandleKeyedPayment)
	requester.RegisterInbound(squarepay.MsgSquareRefund, squareService.HandleRefund)
	if squareService.Enabled() {
		gateways[payment.MethodCard] = squareService.CardRouter(testCard)
		safego.Loop(ctx, "square.refresh", squareService.RunRefresh)
	}
	cardHolds := cardholdApp.NewService(squareRepo, map[string]payment.Holder{squarepay.GatewayName: squareService.Gateway()}, repo, lgr)
	cardHolds.SetNotifier(notifier)
	cardHolds.SetMeters(&meters) // L-3: captures, voids, refunds, failures
	safego.Loop(ctx, "cardhold.worker", cardHolds.Run)
	floorService.OnCardHolds(cardHolds)
	// Website customer accounts (weborder stage 2): phone + code sign-in.
	signinCodes, fakeCodes := customers.CodesFromEnv(cfg.AppConfig.AppEnv, lgr)
	customerService := customers.NewService(repositories.NewCustomerRepository(pool), signinCodes, lgr)
	customerService.SetCardRemover(squareService)
	safego.Loop(ctx, "customers.prune", customerService.RunPrune)
	// The counter's customers (terminal 1d): customer.upsert from the Pi, and
	// customers.changed to a store's Pi when its customers change.
	customerRepo := repositories.NewCustomerRepository(pool)
	floorService.OnCustomers(customerService)
	safego.Loop(ctx, "customers.watch_changes", func(ctx context.Context) { customerService.WatchChanges(ctx, customerRepo, broker, broker) })
	orderPortService := orderport.NewService(pricingService, storeStatusService, menuViewService, menuService, svc,
		repositories.NewChannelOrderRepository(pool), gateways, lgr)
	orderPortService.SetNotifier(notifier) // a failed refund pages the owner
	orderPortService.SetAccounts(customerService)
	safego.Go(ctx, "geo.backfill_stores", geoService.BackfillStores)

	operatorRepo := repositories.NewOperatorRepository(pool, lgr)
	operatorService := operatorApp.NewService(operatorRepo, tenancyService, lgr)
	channelService := channelApp.NewService(repositories.NewChannelRepository(pool), tenancyService, lgr)
	channelService.SetOperators(operatorService) // an operator's sign-in opens /channel/v1 as the store's Call center
	channelService.SetViews(menuViewService)
	// Store websites (weborder stage 2): the storefront's platform key + host → the store's web channel.
	siteService := siteApp.NewService(repositories.NewSiteRepository(pool), tenancyService, os.Getenv("SITE_BASE_DOMAIN"), lgr)
	// The Website page's preview opens on the storefront: https://{host}, or
	// SITE_PREVIEW_ORIGIN (dev: the storefront's dev server, http://{host}:5173).
	if o := os.Getenv("SITE_PREVIEW_ORIGIN"); o != "" {
		siteService.SetPreviewOrigin(o)
	} else if cfg.AppConfig.AppEnv == "dev" {
		siteService.SetPreviewOrigin("http://{host}:5173")
	}
	siteService.SetWebChannels(channelService)
	// The published Website logo prints on the Pi's receipts: store_config
	// carries it, and a publish pushes the fresh store_config.
	storeConfigPub.SetReceiptLogos(siteService)
	siteService.OnPublished(storeConfigPub.Publish)
	channelService.SetSites(siteService)
	// Email receipts for web orders (weborder stage 2).
	mailSender, fakeMail, mailFrom := receipts.SenderFromEnv(ctx, cfg.AppConfig.AppEnv, lgr)
	receiptService := receipts.NewService(repositories.NewReceiptRepository(pool), repo, tenancyService,
		repositories.NewChannelOrderRepository(pool), receipts.StoreFuncs{Host: siteService.PrimaryHost, Reply: customerService.ReplyEmail},
		squareRepo, mailSender, mailFrom, lgr)
	safego.Loop(ctx, "receipts.worker", receiptService.Run)
	// Placements (DURABILITY-PLAN.md): the key before any money moves, the
	// work detached from the request, the sweeper, kitchen_offline.
	// The card nonce a placement keeps while in flight is sealed with
	// SQUARE_TOKEN_KEY, like the stores' OAuth tokens (plaintext when unset).
	placementRepo := repositories.NewPlacementRepository(pool)
	placementRepo.SetTokenBox(squareService)
	orderPortService.SetPlacements(placementRepo, uuid.NewString())
	kitchenLiveness := kitchen.New(broker, printerCache, repositories.NewPlacementRepository(pool))
	orderPortService.SetKitchen(kitchenLiveness)
	orderPortService.SetSweeper(channelService, customerService)
	orderPortService.SetMailer(receiptService)
	orderPortService.SetMeters(&meters)
	safego.Loop(ctx, "orderport.sweeper", orderPortService.Run)
	cardHolds.OnAmountChanged(receiptService.QueueFinal)
	floorService.OnCancelled(receiptService.QueueFinal)

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
	onlineCaster.SetVendorChannel(channelService.BuiltinVendor) // Brygid's orders link to its built-in channel
	casters := map[order.Source]sqsconsumer.Caster{
		order.SourceOnline: onlineCaster,
	}

	sqsConsumer := sqsconsumer.NewConsumer(sqsClient, cfg.SQSConfig.QueueURL, casters, svc, lgr, meters)
	safego.Loop(ctx, "sqs.consumer", sqsConsumer.Start)

	// Store-manager session signer. Secret is stable (Doppler-managed) so
	// sessions survive a blue-green swap; Secure cookies only outside dev.
	sessionSigner, err := session.New(cfg.AppConfig.SessionSecret, cfg.AppConfig.AppEnv != "dev")
	if err != nil {
		lgr.Fatal().Err(err).Msg("failed to build session signer")
	}

	e := echo.New()
	// Whose address a request comes from (M3): main is behind Traefik, so the
	// peer is the proxy; forwarding headers are believed from trusted proxies only.
	ipExtractor, err := clientip.FromEnv(cfg.AppConfig.TrustedProxyCIDRs, cfg.AppConfig.CloudflareCIDRs)
	if err != nil {
		lgr.Fatal().Err(err).Msg("bad trusted proxy configuration")
	}
	e.IPExtractor = ipExtractor
	tLogger := lgr.With().Str("module", "transport").Logger()
	transport.AddDefaultMiddlewares(e, cfg.OTELConfig.ServiceName, &tLogger)
	transport.AddSecurityMiddlewares(e, cfg.AppConfig.AppEnv, cfg.AppConfig.HSTSMaxAge)
	transport.AddRoutes(e, transport.RouterDeps{
		Service:           svc,
		AuthService:       authService,
		TenancyService:    tenancyService,
		Broker:            broker,
		Publisher:         broker,
		AdminAPIKey:       cfg.AppConfig.AdminAPIKey,
		OwnerOffline:      agentSync.OwnerOfflineAlertByMain,
		Version:           version,
		Commit:            commit,
		Pool:              pool,
		SQS:               sqsClient,
		QueueURL:          cfg.SQSConfig.QueueURL,
		Meters:            meters,
		Logger:            tLogger,
		SessionSigner:     sessionSigner,
		Requester:         requester,
		PrinterCache:      printerCache,
		MenuService:       menuService,
		FloorService:      floorService,
		OperatorService:   operatorService,
		ChannelService:    channelService,
		Site:              siteService,
		Square:            squareService,
		CardHolds:         cardHolds,
		SquareFake:        sqFake,
		Customers:         customerService,
		Receipts:          receiptService,
		FakeCodes:         fakeCodes,
		FakeMail:          fakeMail,
		MenuViewService:   menuViewService,
		BankImport:        bankImportService,
		Families:          familiesApp.NewService(repositories.NewFamilyRepository(pool)),
		OptionFlags:       optionflags.NewService(repositories.NewOptionFlagsRepository(pool)),
		StoreStatus:       storeStatusService,
		Kitchen:           kitchenLiveness,
		Pricing:           pricingService,
		OrderPort:         orderPortService,
		Tracking:          trackingService,
		TrackPublicPerMin: handler.TrackPublicPerMin(),
		AgentSync:         agentSync,
		Diag:              diagService,
		Geo:               geoService,
		AlertSender:       ownerPager,
		AppEnv:            cfg.AppConfig.AppEnv,
		ReadyCache:        time.Duration(cfg.AppConfig.ReadyCacheSeconds) * time.Second,
		DevNotices:        devNotices,
		Staff:             staffService,
		Callcenter:        callcenterService,
		DayClose:          dayCloseService,
		Audit:             repositories.NewAuditRepository(pool),
	})

	serverLogger := lgr.With().Str("module", "server").Logger()
	transport.StartServer(ctx, e, cfg, &serverLogger)
}

// dsnValue quotes one keyword/value connection-string value (libpq rules), so
// a password with a space, quote or backslash can't break the DSN.
func dsnValue(v string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}
