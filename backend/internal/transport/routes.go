// Package transport wires every HTTP-facing piece of main/: router, default
// middlewares, route registration, and graceful start/stop.
//
// Routes are a single source of truth: `routeTable` returns a []routeDef that
// `AddRoutes` both REGISTERS with Echo and serializes at GET /__routes (so the
// console's routes viewer can't drift from what's actually served). Layout:
//
//	/api/v1/*    — device-authenticated, scope-filtered runtime API
//	/admin/v1/*  — admin-authenticated tenancy management (single bearer)
//	/store/v1/*  — store-manager surface: browser managers use the session cookie
//	               (POST /session + /me/*); the agent uses X-STORE-API-KEY
//	               (store-config), provisioned by an admin-minted device token
//	/app         — the embedded compiled SvelteKit UI (SPA); /admin + /store 302
//	               redirect into /app/admin + /app/store
package transport

import (
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	floorApp "quiccpos/main/internal/app/floor"
	"strings"

	"quiccpos/main/internal/app/agentsync"
	"quiccpos/main/internal/app/bankimport"
	cardholdApp "quiccpos/main/internal/app/cardhold"
	channelApp "quiccpos/main/internal/app/channel"
	"quiccpos/main/internal/app/customers"
	diagApp "quiccpos/main/internal/app/diag"
	geoApp "quiccpos/main/internal/app/geo"
	menuApp "quiccpos/main/internal/app/menu"
	menuviewApp "quiccpos/main/internal/app/menuview"
	operatorApp "quiccpos/main/internal/app/operator"
	orderSvc "quiccpos/main/internal/app/order"
	"quiccpos/main/internal/app/orderport"
	pricingApp "quiccpos/main/internal/app/pricing"
	"quiccpos/main/internal/app/printerstatus"
	"quiccpos/main/internal/app/receipts"
	siteApp "quiccpos/main/internal/app/site"
	"quiccpos/main/internal/app/squarepay"
	staffApp "quiccpos/main/internal/app/staff"
	storestatusApp "quiccpos/main/internal/app/storestatus"
	tenancyApp "quiccpos/main/internal/app/tenancy"
	"quiccpos/main/internal/app/tracking"
	"quiccpos/main/internal/domain/notify"
	"quiccpos/main/internal/infra/mail"
	"quiccpos/main/internal/observability"
	"quiccpos/main/internal/rpc"
	"quiccpos/main/internal/transport/handler"
	appMiddleware "quiccpos/main/internal/transport/middleware"
	"quiccpos/main/internal/transport/session"
	"quiccpos/main/internal/transport/webapp"

	authSvc "quiccpos/main/internal/app/auth"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
	echomw "github.com/labstack/echo/v5/middleware"
	"github.com/rs/zerolog"
)

const (
	apiPrefix   = "/api/v1"
	adminPrefix = "/admin/v1"
	adminUIPath = "/admin"
	storePrefix = "/store/v1"
	storeUIPath = "/store"
	// channelPrefix is main 3.0's channel API (the ports).
	channelPrefix = "/channel/v1"
)

type RouterDeps struct {
	Service        *orderSvc.Service
	AuthService    *authSvc.Service
	TenancyService *tenancyApp.Service
	Broker         handler.SSEBroker
	// Publisher pushes frames to a scope's connected agents (live store_config
	// after location/printer edits). The concrete *sse.Broker satisfies it.
	Publisher   handler.FramePublisher
	AdminAPIKey string
	// Version + Commit are injected from cmd/api/main.go's ldflags-set vars.
	// Surfaced via GET /api/v1/version so ops can `curl` and confirm what's
	// actually running.
	Version string
	Commit  string
	// Pool + SQS + QueueURL feed the /api/v1/ready probe (DB ping + SQS
	// poke). Pre-existing dependencies — just routed through here so the
	// readiness handler can call them without a second wiring path.
	Pool     *pgxpool.Pool
	SQS      *sqs.Client
	QueueURL string
	Meters   observability.Meters
	Logger   zerolog.Logger
	// SessionSigner signs the store-manager browser session cookie.
	SessionSigner *session.Signer
	// Requester is the duplex-channel RPC core (main↔agent).
	Requester *rpc.Requester
	// PrinterCache holds the agent-reported live printer roster per store.
	PrinterCache *printerstatus.Cache
	// FloorService applies the agent's floor events and backs the store floor view.
	FloorService *floorApp.Service
	// MenuService backs the store-manager menu editor.
	MenuService *menuApp.Service
	// OperatorService backs the operator accounts + read-only menu API.
	OperatorService *operatorApp.Service
	// Tracking follows channel orders (main 3.0).
	Tracking *tracking.Service
	// TrackPublicPerMin: /track/v1 lookups per IP and minute (0 = the default, 120).
	TrackPublicPerMin int
	// OrderPort places channel orders (main 3.0).
	OrderPort *orderport.Service
	// Pricing backs pricing settings, deals and quotes (main 3.0).
	Pricing *pricingApp.Service
	// StoreStatus backs a store's ordering status (main 3.0).
	StoreStatus *storestatusApp.Service
	// Kitchen answers whether a store's Pi can get an order now (kitchen_offline
	// on /channel/v1/store, DURABILITY-PLAN.md).
	Kitchen handler.KitchenCheck
	// MenuViewService backs the menu bank + views (main 3.0).
	MenuViewService *menuviewApp.Service
	// BankImport applies / exports a store's bank document (weborder converter).
	BankImport *bankimport.Service
	// ChannelService backs sales channels and the /channel/v1 key (main 3.0).
	ChannelService *channelApp.Service
	// Site backs store websites: platform keys, hosts, /channel/v1/site.
	Site *siteApp.Service
	// Square connects stores to Square; CardHolds captures online card holds.
	Square    *squarepay.Service
	CardHolds *cardholdApp.Service
	// Customers backs website accounts; Receipts emails web order receipts.
	Customers *customers.Service
	Receipts  *receipts.Service
	FakeCodes *customers.FakeCodes // dev / staging fake sign-in codes (nil otherwise)
	FakeMail  *mail.Fake           // dev / staging fake mail (nil otherwise)
	// SquareFake is the in-process fake Square (dev / staging with SQUARE_ENV=fake; nil otherwise).
	SquareFake http.Handler
	// AgentSync backs agent link / restore / backups / fleet health.
	AgentSync *agentsync.Service
	// Diag backs tablet diagnostics (report tokens, reports, admin view).
	Diag *diagApp.Service
	// Geo backs delivery maps: store pin, published map, store geocoding.
	Geo *geoApp.Service
	// AlertSender sends the store owner's test alert (main's owner pager).
	AlertSender handler.AlertSender
	// AppEnv gates dev-only routes (the geo test-provider override).
	AppEnv string
	// DevNotices records owner notices on dev (GET /admin/v1/dev/notices).
	DevNotices *notify.Recorder
	// Staff backs the counter terminal's staff + terminal settings (TERMINAL-PLAN 0d).
	Staff *staffApp.Service
}

// RouteInfo is the JSON shape served at GET /__routes (shared across all
// modules: method, path, auth, description).
type RouteInfo struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Auth        string `json:"auth"`
	Description string `json:"description"`
}

// routeDef is one route: its public metadata plus the handler + per-route
// middleware used to register it.
type routeDef struct {
	RouteInfo
	Handler echo.HandlerFunc
	MW      []echo.MiddlewareFunc
}

func def(method, path, auth, desc string, h echo.HandlerFunc, mw ...echo.MiddlewareFunc) routeDef {
	return routeDef{RouteInfo{method, path, auth, desc}, h, mw}
}

// AddRoutes registers every route from the table and adds GET /__routes.
func AddRoutes(e *echo.Echo, d RouterDeps) {
	d.Logger.Info().Str("prefix", apiPrefix).Msg("registering routes")

	defs := routeTable(d)
	for _, r := range defs {
		e.Add(r.Method, r.Path, r.Handler, r.MW...)
	}
	if d.SquareFake != nil {
		// The in-process fake Square (never in prod; not in /__routes).
		e.Any("/__fake/square/*", echo.WrapHandler(d.SquareFake))
	}

	infos := make([]RouteInfo, 0, len(defs)+1)
	for _, r := range defs {
		infos = append(infos, r.RouteInfo)
	}
	infos = append(infos, RouteInfo{"GET", "/__routes", "none", "This route list (method/path/auth/description)"})
	e.GET("/__routes", func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{
			"module": "main", "version": d.Version, "routes": infos,
		})
	})
}

// routeTable is the single source of truth. Add / rename / re-gate a route HERE.
func routeTable(d RouterDeps) []routeDef {
	device := appMiddleware.APIKeyAuth(d.AuthService, d.Meters)
	agentOnly := appMiddleware.RequireDeviceKind(tenancyApp.DeviceKindAgent)
	admin := appMiddleware.AdminAuth(d.AdminAPIKey)
	storeSession := appMiddleware.StoreSessionAuth(d.SessionSigner)
	operatorAuth := appMiddleware.OperatorAuth(d.OperatorService, d.Meters)
	channelAuth := appMiddleware.ChannelAuth(d.ChannelService)
	// The channel API alone is gzipped (a menu is large); legacy routes are
	// left byte-for-byte as deployed clients know them.
	channelGzip := echomw.GzipWithConfig(echomw.GzipConfig{MinLength: 1024})
	// The tablet report key guards exactly one route (diag-reports) and is
	// never combined with `device` — it must not reach orders or anything else.
	reportKey := appMiddleware.ReportKeyAuth(d.Diag)

	orderH := handler.NewOrderHandler(d.Service, d.Logger)
	sseH := handler.NewSSEHandler(d.Broker, d.TenancyService, d.Logger, d.Meters)
	cfgPub := handler.NewStoreConfigPublisher(d.Publisher, d.TenancyService, d.Logger)
	adminH := handler.NewAdminHandler(d.TenancyService, d.Service, cfgPub, d.SessionSigner, d.Logger)
	storeH := handler.NewStoreHandler(d.TenancyService, d.SessionSigner, cfgPub, d.Logger)
	agentRPCH := handler.NewAgentRPCHandler(d.Requester, d.Logger)
	printerStatusH := handler.NewPrinterStatusHandler(d.PrinterCache, d.Requester, d.Meters, d.Logger)
	menuH := handler.NewMenuHandler(d.MenuService, d.Logger)
	floorH := handler.NewFloorHandler(d.Service, d.FloorService, d.Logger)
	operatorH := handler.NewOperatorHandler(d.OperatorService, d.MenuService, d.Meters, d.Logger)
	channelH := handler.NewChannelHandler(d.ChannelService, d.TenancyService, d.Logger)
	siteH := handler.NewSiteHandler(d.Site, d.Logger)
	squareH := handler.NewSquareHandler(d.Square, d.CardHolds, d.AppEnv, d.Logger)
	customersH := handler.NewCustomersHandler(d.Customers, d.TenancyService.GetLocation, d.Logger)
	accountH := handler.NewAccountHandler(d.Customers, d.OrderPort, d.Receipts, d.FakeCodes, d.FakeMail, d.AppEnv, d.Logger)
	guest := appMiddleware.CustomerAuth(d.Customers.Authenticate, false)
	member := appMiddleware.CustomerAuth(d.Customers.Authenticate, true)
	viewH := handler.NewMenuViewHandler(d.MenuViewService, d.Logger)
	bankDocH := handler.NewBankImportHandler(d.BankImport, d.Logger)
	statusH := handler.NewStoreStatusHandler(d.StoreStatus, d.AppEnv, d.Logger)
	staffH := handler.NewStaffHandler(d.Staff, d.MenuViewService, d.Pricing, d.MenuService, d.Logger)
	if d.Kitchen != nil {
		statusH.SetKitchen(d.Kitchen)
	}
	pricingH := handler.NewPricingHandler(d.Pricing, d.Logger)
	orderPortH := handler.NewOrderPortHandler(d.OrderPort, d.Logger)
	orderPortH.SetDev(d.AppEnv, d.DevNotices)
	trackingH := handler.NewTrackingHandler(d.Tracking, d.TrackPublicPerMin, d.Logger)
	if d.ChannelService != nil {
		trackingH.SetSiteAuth(d.ChannelService.AuthenticateSite)
	}
	trackingH.SetDev(d.AppEnv)
	readiness := handler.NewReadinessHandler(d.Pool, d.SQS, d.QueueURL, d.Logger)
	appRelH := handler.NewAppReleaseHandler(d.Pool, d.Logger)
	agentLinkH := handler.NewAgentLinkHandler(d.AgentSync, d.Logger)
	kitchenH := handler.NewKitchenStatusHandler(d.TenancyService.ListLocations, d.Kitchen, d.Logger)
	diagH := handler.NewDiagHandler(d.Diag, d.Logger)
	agentUpdateH := handler.NewAgentUpdateHandler(d.AgentSync, d.Requester, d.Logger)
	alertsH := handler.NewAlertsHandler(d.TenancyService, d.AlertSender, d.Logger)
	geoH := handler.NewGeoHandler(d.TenancyService, d.Geo, cfgPub, d.AppEnv, d.Logger)
	if d.Geo != nil {
		adminH.OnLocationSaved(d.Geo.GeocodeStoreAsync)
		storeH.OnLocationSaved(d.Geo.GeocodeStoreAsync)
	}

	appFS := webapp.FS()
	appBuilt := webapp.Built()
	serveAppH := func(c *echo.Context) error { return serveApp(c, appFS, appBuilt) }

	versionBody := map[string]string{"service": "quicc-main", "version": d.Version, "commit": d.Commit}
	health := func(c *echo.Context) error { return c.String(http.StatusOK, "OK") }
	version := func(c *echo.Context) error { return c.JSON(http.StatusOK, versionBody) }

	return []routeDef{
		// System (unauthenticated).
		def("GET", "/health", "none", "Liveness — 200 if the process is up", health),
		def("GET", apiPrefix+"/version", "none", "Build identifiers (version + commit)", version),
		def("GET", apiPrefix+"/ready", "none", "Readiness — pings Postgres + pokes SQS", readiness.Check),

		// Orders — device-authenticated, scope-filtered.
		def("GET", apiPrefix+"/orders", "device", "Paginated order list (q/status/from/to/sort)", orderH.GetOrders, device),
		def("GET", apiPrefix+"/orders/by-source/:source/:source_event_id", "device", "Lookup by origin identity", orderH.GetBySourceEvent, device),
		def("GET", apiPrefix+"/orders/:id", "device", "Lookup by canonical UUID", orderH.GetByID, device),

		// Events — device-authenticated SSE.
		def("GET", apiPrefix+"/events/orders", "device", "SSE stream (store_config handshake + orders) — agent keys only", sseH.StreamOrders, device, agentOnly),

		// Admin — single shared bearer.
		def("POST", adminPrefix+"/app-releases", "admin", "Upload a tablet-app APK (?app=quibase&version_code&version_name; body = APK)", appRelH.Upload, admin),
		def("DELETE", adminPrefix+"/app-releases/:code", "admin", "Delete a tablet-app release (?app=quibase)", appRelH.Delete, admin),
		def("GET", adminPrefix+"/app-releases", "admin", "List tablet-app releases (?app=quibase), newest first", appRelH.List, admin),
		def("GET", adminPrefix+"/agents/health", "admin", "Every location's agent health + connected state (fleet view)", agentLinkH.Fleet, admin),
		def("GET", adminPrefix+"/kitchen", "admin", "Every location's kitchen_offline (the Pi liveness check behind 409 kitchen_offline) + away_since", kitchenH.AdminList, admin),
		def("GET", adminPrefix+"/agent-backups", "admin", "Stored agent DB backups (?location_id)", agentLinkH.ListBackups, admin),
		def("GET", adminPrefix+"/agent-backups/:id", "admin", "Download one agent DB backup", agentLinkH.GetBackup, admin),
		def("GET", adminPrefix+"/overview", "admin", "Flat dump of tenants/locations/devices", adminH.Overview, admin),
		def("POST", adminPrefix+"/tenants", "admin", "Create a tenant (optional username/password/store_id)", adminH.CreateTenant, admin),
		def("GET", adminPrefix+"/tenants", "admin", "List all tenants", adminH.ListTenants, admin),
		def("PATCH", adminPrefix+"/tenants/:id/config", "admin", "Edit a tenant's config blob", adminH.UpdateTenantConfig, admin),
		def("PATCH", adminPrefix+"/tenants/:id/credentials", "admin", "Set/rotate a tenant's store-manager login", adminH.SetTenantCredentials, admin),
		def("PATCH", adminPrefix+"/tenants/:id/store-id", "admin", "Set/clear a tenant's external vendor store_id (online order routing key)", adminH.SetTenantStoreID, admin),
		def("POST", adminPrefix+"/tenants/:id/store-session", "admin", "Open this tenant's store as its manager (sets a 2h store session cookie; audited)", adminH.OpenStoreSession, admin),
		def("DELETE", adminPrefix+"/tenants/:id", "admin", "Cascade-delete a tenant + its locations/orders/devices", adminH.DeleteTenant, admin),
		def("GET", adminPrefix+"/orders", "admin", "Cross-tenant order list (?source_event_id/tenant_id/limit)", adminH.ListOrders, admin),
		def("DELETE", adminPrefix+"/orders", "admin", "Bulk-delete orders (?tenant_id, absent=all)", adminH.DeleteOrders, admin),
		def("DELETE", adminPrefix+"/orders/:id", "admin", "Delete a single order + children (cleanup)", adminH.DeleteOrder, admin),
		def("GET", adminPrefix+"/locations", "admin", "List all locations", adminH.ListLocations, admin),
		def("PATCH", adminPrefix+"/locations/:id", "admin", "Edit a location (full replace)", adminH.UpdateLocation, admin),
		def("PUT", adminPrefix+"/locations/:id/pin", "admin", "Place a store pin by hand {lat, lng} (geo_source manual; pushes store_config)", geoH.AdminSetPin, admin),
		def("PATCH", adminPrefix+"/locations/:id/map", "admin", "Publish the store's offline map {url, sha256, version, bbox} ({url:\"\"} clears; pushes store_config)", geoH.AdminSetMap, admin),
		def("GET", adminPrefix+"/delivery-runs", "admin", "A location's driver runs, newest first (?tenant_id&location_id&limit=50)", floorH.AdminDeliveryRuns, admin),
		def("GET", adminPrefix+"/geo", "admin", "Active geocoding/routing providers", geoH.AdminStatus, admin),
		def("POST", adminPrefix+"/geo/test-providers", "admin", "DEV ONLY: point the geo providers at a test fake {census_url, nominatim_url, osrm_url}", geoH.DevSetProviders, admin),
		def("DELETE", adminPrefix+"/geo/test-providers", "admin", "DEV ONLY: restore the env geo providers", geoH.DevResetProviders, admin),
		def("POST", adminPrefix+"/store-status/restock", "admin", "DEV ONLY: bring a location's 86'd items back now {location_id}", statusH.DevRestock, admin),
		def("POST", adminPrefix+"/devices", "admin", "Mint an agent device (cleartext key once)", adminH.MintAgentDevice, admin),
		def("GET", adminPrefix+"/devices", "admin", "List all devices", adminH.ListDevices, admin),
		def("POST", adminPrefix+"/devices/:id/revoke", "admin", "Revoke a device (tombstone)", adminH.RevokeDevice, admin),
		def("DELETE", adminPrefix+"/devices/:id", "admin", "Hard-delete a device row", adminH.DeleteDevice, admin),
		def("POST", adminPrefix+"/agent/ping", "admin", "Round-trip an agent over the duplex channel (debug)", agentRPCH.PingAgent, admin),
		def("POST", adminPrefix+"/agent/update", "admin", "Ask a location's Pi to update {tenant_id, location_id, force} (after close; force = once idle)", agentUpdateH.Update, admin),
		def("POST", adminPrefix+"/agent/rollback", "admin", "Ask a location's Pi to roll back to its previous agent {tenant_id, location_id}", agentUpdateH.Rollback, admin),
		def("GET", adminPrefix+"/agent-updates", "admin", "Pi update history, newest first (?location_id&limit=20)", agentUpdateH.List, admin),

		// Admin — tablet diagnostics (reports + tablet report tokens).
		def("GET", adminPrefix+"/diag-reports", "admin", "Tablet diagnostics reports, newest first (?tenant_id&kind&outcome&unresolved&limit)", diagH.AdminList, admin),
		def("GET", adminPrefix+"/diag-reports/:id", "admin", "One diagnostics report in full (uuid or 8-char prefix)", diagH.AdminGet, admin),
		def("PATCH", adminPrefix+"/diag-reports/:id", "admin", "Mark a diagnostics report resolved / reopen it {resolved}", diagH.AdminResolve, admin),
		def("GET", adminPrefix+"/tablet-tokens", "admin", "Tablet report tokens (?location_id), newest first", diagH.AdminListTokens, admin),
		def("POST", adminPrefix+"/tablet-tokens/:id/revoke", "admin", "Revoke a tablet report token", diagH.AdminRevokeToken, admin),

		// Admin — operator (call-center) accounts.
		def("GET", adminPrefix+"/operators", "admin", "List operators + their allowed locations", operatorH.AdminList, admin),
		def("POST", adminPrefix+"/operators", "admin", "Create an operator (username/password)", operatorH.AdminCreate, admin),
		def("PATCH", adminPrefix+"/operators/:id", "admin", "Edit display name / rotate password", operatorH.AdminUpdate, admin),
		def("DELETE", adminPrefix+"/operators/:id", "admin", "Delete an operator (cascades tokens + locations)", operatorH.AdminDelete, admin),
		def("PUT", adminPrefix+"/operators/:id/locations", "admin", "Set an operator's allowed locations", operatorH.AdminSetLocations, admin),
		def("POST", adminPrefix+"/operators/:id/tokens/revoke", "admin", "Revoke all of an operator's tokens", operatorH.AdminRevokeTokens, admin),

		// Sales channels (main 3.0): admin-managed, each with its own /channel/v1 key.
		def("GET", adminPrefix+"/channels", "admin", "List sales channels (?location_id=)", channelH.AdminList, admin),
		def("POST", adminPrefix+"/channels", "admin", "Create a channel {location_id, kind, name, permissions?} → key once", channelH.AdminCreate, admin),
		def("GET", adminPrefix+"/channels/:id", "admin", "One channel", channelH.AdminGet, admin),
		def("PATCH", adminPrefix+"/channels/:id", "admin", "Rename / set permissions", channelH.AdminUpdate, admin),
		def("POST", adminPrefix+"/channels/:id/rotate", "admin", "Issue a new key (old one stops) → key once", channelH.AdminRotate, admin),
		def("POST", adminPrefix+"/channels/:id/revoke", "admin", "Revoke the channel's key", channelH.AdminRevoke, admin),
		def("DELETE", adminPrefix+"/channels/:id", "admin", "Delete a channel", channelH.AdminDelete, admin),

		// Store websites (weborder stage 2): the storefront's platform keys + each store's hosts.
		def("GET", adminPrefix+"/platform-keys", "admin", "List the storefront's platform keys", siteH.AdminListKeys, admin),
		def("POST", adminPrefix+"/platform-keys", "admin", "Issue a platform key {name} → key once", siteH.AdminCreateKey, admin),
		def("POST", adminPrefix+"/platform-keys/:id/revoke", "admin", "Revoke a platform key", siteH.AdminRevokeKey, admin),
		def("GET", adminPrefix+"/locations/:id/site-domains", "admin", "A store's website hosts", siteH.AdminDomains, admin),
		def("POST", adminPrefix+"/locations/:id/site-domains", "admin", "Add a host {host, primary} (platform subdomains verified at once)", siteH.AdminAddDomain, admin),
		def("PATCH", adminPrefix+"/site-domains/:host", "admin", "Mark a host verified {verified}", siteH.AdminVerifyDomain, admin),

		// Customers (terminal stage 0c): the bulk import (SoftTouch converter, admin only).
		def("POST", adminPrefix+"/locations/:id/customers/import", "admin", "Upsert ≤ 5,000 customers by phone (NDJSON or JSON; ?dry_run=1) → counts", customersH.Import, admin),

		// Online card holds (weborder stage 2).
		def("GET", adminPrefix+"/card-holds/:order_id", "admin", "An order's card hold", squareH.AdminHold, admin),
		def("POST", adminPrefix+"/card-holds/:order_id/capture", "admin", "Capture an order's card hold now", squareH.AdminCapture, admin),
		def("POST", adminPrefix+"/card-holds/:order_id/due", "admin", "Dev only: make the hold's safety capture due and run the worker", squareH.AdminDue, admin),
		def("POST", adminPrefix+"/card-holds/tick", "admin", "Dev only: run the capture worker once", squareH.AdminTick, admin),
		def("GET", adminPrefix+"/orders/:id/receipts", "admin", "An order's email receipts", accountH.AdminReceipts, admin),
		def("GET", adminPrefix+"/dev/signin-code", "admin", "Dev only: the fake sign-in code for ?phone=", accountH.DevCode, admin),
		def("GET", adminPrefix+"/dev/mail", "admin", "Dev only: fake mail sent ?to=", accountH.DevMail, admin),
		def("POST", adminPrefix+"/dev/receipts/tick", "admin", "Dev only: send queued receipts now", accountH.DevReceiptsTick, admin),
		def("POST", adminPrefix+"/dev/track-limit", "admin", "Dev only: a storefront's own per-customer /track/v1 limit ({key, per_min}; 0 = shared again)", trackingH.DevTrackLimit, admin),
		def("POST", adminPrefix+"/dev/placements/sweep", "admin", "Dev only: run the placement sweeper once (nothing waits to go stale)", orderPortH.DevSweep, admin),
		def("POST", adminPrefix+"/dev/placements/orphan", "admin", "Dev only: leave a pending placement as a main that died mid-work would", orderPortH.DevOrphan, admin),
		def("GET", adminPrefix+"/dev/notices", "admin", "Dev only: the owner notices main sent ?location_id=", orderPortH.DevNotices, admin),

		// Square → main (weborder stage 2): the owner comes back from Square; Square's events.
		def("GET", "/square/oauth/callback", "none", "Square OAuth callback (state-checked) → the SPA", squareH.OAuthCallback),
		def("POST", "/square/webhook", "none", "Square webhooks (HMAC-signed)", squareH.Webhook),

		// Web app (compiled SvelteKit SPA) — served under /app; /admin + /store
		// redirect in for back-compat. Assets live under /app/_app/*.
		def("GET", "/app", "none", "SvelteKit app shell", serveAppH),
		def("GET", "/app/*", "none", "SvelteKit app (SPA fallback + hashed assets)", serveAppH),
		def("GET", adminUIPath, "none", "Redirect → /app/admin", redirectTo("/app/admin")),
		def("GET", adminUIPath+"/", "none", "Redirect → /app/admin", redirectTo("/app/admin")),

		// Store — agent surface: device-keyed store-config + self-revoke. The
		// agent is now provisioned by an admin-minted token pasted into its setup
		// screen (verified by a device-authed GET here), so the old username/
		// password mint login is gone.
		def("GET", storePrefix+"/store-config", "device", "This store's config (scope from the key) — agent keys only", storeH.GetStoreConfig, device, agentOnly),
		def("POST", storePrefix+"/agent/link", "none", "Link a blank agent with the store manager login → agent key + secrets (revokes the old agent; throttled)", agentLinkH.Link),
		def("GET", storePrefix+"/agent/restore", "device", "Open batch for a replacement agent (state + orders + drivers) — agent keys only", agentLinkH.Restore, device, agentOnly),
		def("GET", storePrefix+"/agent/credential", "device", "Manager login hash for offline web-UI unlock — agent keys only", agentLinkH.Credential, device, agentOnly),
		def("GET", storePrefix+"/agent/secrets", "device", "Current Pushover secrets {pushover_app_token, pushover_user_key, pushover_boss_key, pushover_admin_devices} (no-store) — agent keys only", agentLinkH.Secrets, device, agentOnly),
		def("POST", storePrefix+"/agent/backup", "device", "Upload the agent's nightly SQLite copy (newest 2 kept) — agent keys only", agentLinkH.Backup, device, agentOnly),
		def("POST", storePrefix+"/agent/rpc", "device", "Duplex channel: agent→main replies + reports — agent keys only", agentRPCH.Handle, device, agentOnly),
		def("GET", storePrefix+"/agent/app/latest", "device", "Newest tablet-app release (?app=quibase; 204 none) — agent keys only", appRelH.Latest, device, agentOnly),
		def("GET", storePrefix+"/agent/app/releases/:code/apk", "device", "Download a tablet-app APK (?app=quibase) — agent keys only", appRelH.APK, device, agentOnly),
		def("GET", storePrefix+"/agent/customers", "device", "The store's customer changes for the Pi's offline copy (?since=<cursor>&limit) — agent keys only", customersH.Feed, device, agentOnly),
		def("GET", storePrefix+"/agent/terminal-config", "device", "The counter terminal's settings + active staff with PIN hashes (ETag) — agent keys only", staffH.TerminalConfig, device, agentOnly),
		def("GET", storePrefix+"/agent/counter-menu", "device", "The counter view's build + layout + the counter's pricing (ETag, gzip) — agent keys only", staffH.CounterMenu, device, agentOnly, channelGzip),
		def("POST", storePrefix+"/agent/revoke", "device", "Agent self-revoke (kills its own key) — agent keys only", storeH.AgentSelfRevoke, device, agentOnly),
		def("POST", storePrefix+"/agent/tablet-tokens", "device", "Mint a tablet's diagnostics report token {tablet_id, tablet_name} — agent keys only", diagH.MintTabletToken, device, agentOnly),
		def("DELETE", storePrefix+"/agent/tablet-tokens/:tablet_id", "device", "Revoke a tablet's report token (idempotent) — agent keys only", diagH.RevokeTabletToken, device, agentOnly),

		// Tablet — direct diagnostics upload when the Pi is down. Auth is ONLY
		// the tablet report key (X-Quibase-Report-Key); it opens nothing else.
		def("POST", storePrefix+"/tablet/diag-reports", "report-key", "Tablet diagnostics report v1 (≤256 KB) sent straight to main", diagH.PostReport, reportKey),

		// Store — browser manager session (signed httpOnly cookie).
		def("POST", storePrefix+"/session", "none", "Manager login → set the scoped session cookie", storeH.SessionLogin),
		def("DELETE", storePrefix+"/session", "store-session", "Manager logout → clear the cookie", storeH.SessionLogout, storeSession),
		def("GET", storePrefix+"/session", "store-session", "Whoami — the manager's tenant + location", storeH.SessionWhoami, storeSession),

		// Store — session-scoped self-service (location from the cookie, not the URL).
		def("GET", storePrefix+"/me/location", "store-session", "This manager's location", storeH.MeGetLocation, storeSession),
		def("PATCH", storePrefix+"/me/location", "store-session", "Edit this manager's location (full replace incl. config; pushes store_config; bad Pushover device name → 400 bad_device)", storeH.MeUpdateLocation, storeSession),
		def("POST", storePrefix+"/me/alerts/test", "store-session", "Send one test Pushover to the saved owner phones (location config pushover_owner_devices; 1 per 10 s)", alertsH.MeTest, storeSession),
		def("PUT", storePrefix+"/me/location/pin", "store-session", "Place the store pin by hand {lat, lng} (Store › Details map)", geoH.MeSetPin, storeSession),
		def("GET", storePrefix+"/me/orders", "store-session", "Recent orders with the agent-synced floor status (?limit)", floorH.MeListOrders, storeSession),
		def("GET", storePrefix+"/me/delivery-runs", "store-session", "Driver runs sent (AUTO suggestion vs sent), newest first (?limit=50)", floorH.MeDeliveryRuns, storeSession),
		def("GET", storePrefix+"/me/floor", "store-session", "Driver roster + last agent floor sync", floorH.MeGetFloor, storeSession),
		def("GET", storePrefix+"/me/printers/live", "store-session", "Agent-reported live printer roster + reachability", printerStatusH.MeGetLive, storeSession),
		def("GET", storePrefix+"/me/printers/ip-changes", "store-session", "Printer IP changes, newest first (?limit=20) — the Pi following a printer + manager edits", storeH.MeListPrinterIPChanges, storeSession),
		def("GET", storePrefix+"/me/printers", "store-session", "List this location's printers", storeH.MeListPrinters, storeSession),
		def("POST", storePrefix+"/me/printers", "store-session", "Add a printer", storeH.MeCreatePrinter, storeSession),
		def("PATCH", storePrefix+"/me/printers/:id", "store-session", "Edit a printer (must be this location's)", storeH.MeUpdatePrinter, storeSession),
		def("DELETE", storePrefix+"/me/printers/:id", "store-session", "Delete a printer (must be this location's)", storeH.MeDeletePrinter, storeSession),
		def("GET", storePrefix+"/me/devices", "store-session", "List this location's devices", storeH.MeListDevices, storeSession),
		def("POST", storePrefix+"/me/devices", "store-session", "Mint a non-agent device", storeH.MeMintDevice, storeSession),
		def("POST", storePrefix+"/me/devices/:id/revoke", "store-session", "Revoke a device (must be this location's)", storeH.MeRevokeDevice, storeSession),

		// Store — menu editor (session-scoped). menus → categories → items;
		// reusable modifier groups attached M:N.
		def("GET", storePrefix+"/me/menu/unmatched", "store-session", "Recent vendor item names that don't route to a printer (?days=14)", menuH.MeUnmatched, storeSession),
		def("GET", storePrefix+"/me/menu/export.csv", "store-session", "Download the menu as CSV (menu,category,item,price,printer,description)", menuH.MeExportCSV, storeSession),
		def("POST", storePrefix+"/me/menu/import", "store-session", "Import a menu CSV (?dry_run=1 previews, ?replace=1 deletes existing menus first)", menuH.MeImportCSV, storeSession),
		def("GET", storePrefix+"/me/menu/menus", "store-session", "List this location's menus", menuH.MeListMenus, storeSession),
		def("POST", storePrefix+"/me/menu/menus", "store-session", "Create a menu", menuH.MeCreateMenu, storeSession),
		def("POST", storePrefix+"/me/menu/menus/reorder", "store-session", "Reorder menus", menuH.MeReorderMenus, storeSession),
		def("PATCH", storePrefix+"/me/menu/menus/:id", "store-session", "Edit a menu", menuH.MeUpdateMenu, storeSession),
		def("DELETE", storePrefix+"/me/menu/menus/:id", "store-session", "Delete a menu", menuH.MeDeleteMenu, storeSession),
		def("GET", storePrefix+"/me/menu/menus/:id/categories", "store-session", "List a menu's categories", menuH.MeListCategories, storeSession),
		def("POST", storePrefix+"/me/menu/menus/:id/categories", "store-session", "Add a category", menuH.MeCreateCategory, storeSession),
		def("POST", storePrefix+"/me/menu/categories/reorder", "store-session", "Reorder categories", menuH.MeReorderCategories, storeSession),
		def("PATCH", storePrefix+"/me/menu/categories/:id", "store-session", "Edit a category", menuH.MeUpdateCategory, storeSession),
		def("DELETE", storePrefix+"/me/menu/categories/:id", "store-session", "Delete a category", menuH.MeDeleteCategory, storeSession),
		def("GET", storePrefix+"/me/menu/categories/:id/items", "store-session", "List a category's items", menuH.MeListItems, storeSession),
		def("POST", storePrefix+"/me/menu/categories/:id/items", "store-session", "Add an item", menuH.MeCreateItem, storeSession),
		def("POST", storePrefix+"/me/menu/items/reorder", "store-session", "Reorder items", menuH.MeReorderItems, storeSession),
		def("PATCH", storePrefix+"/me/menu/items/:id", "store-session", "Edit an item", menuH.MeUpdateItem, storeSession),
		def("DELETE", storePrefix+"/me/menu/items/:id", "store-session", "Delete an item", menuH.MeDeleteItem, storeSession),
		def("PATCH", storePrefix+"/me/menu/items/:id/printer", "store-session", "Assign an item's printer", menuH.MeAssignItemPrinter, storeSession),
		def("GET", storePrefix+"/me/menu/items/:id/modifier-groups", "store-session", "List an item's modifier groups", menuH.MeListItemGroups, storeSession),
		def("POST", storePrefix+"/me/menu/items/:id/modifier-groups", "store-session", "Attach a modifier group to an item", menuH.MeAttachGroup, storeSession),
		def("DELETE", storePrefix+"/me/menu/items/:id/modifier-groups/:groupId", "store-session", "Detach a modifier group", menuH.MeDetachGroup, storeSession),
		def("GET", storePrefix+"/me/menu/modifier-groups", "store-session", "List reusable modifier groups", menuH.MeListGroups, storeSession),
		def("POST", storePrefix+"/me/menu/modifier-groups", "store-session", "Create a modifier group", menuH.MeCreateGroup, storeSession),
		def("PATCH", storePrefix+"/me/menu/modifier-groups/:id", "store-session", "Edit a modifier group", menuH.MeUpdateGroup, storeSession),
		def("DELETE", storePrefix+"/me/menu/modifier-groups/:id", "store-session", "Delete a modifier group (detaches from all items)", menuH.MeDeleteGroup, storeSession),
		def("GET", storePrefix+"/me/menu/modifier-groups/:id/options", "store-session", "List a group's options", menuH.MeListOptions, storeSession),
		def("POST", storePrefix+"/me/menu/modifier-groups/:id/options", "store-session", "Add an option", menuH.MeCreateOption, storeSession),
		def("POST", storePrefix+"/me/menu/modifier-groups/:id/options/reorder", "store-session", "Reorder options", menuH.MeReorderOptions, storeSession),
		def("PATCH", storePrefix+"/me/menu/modifier-groups/:id/options/:optId", "store-session", "Edit an option", menuH.MeUpdateOption, storeSession),
		def("DELETE", storePrefix+"/me/menu/modifier-groups/:id/options/:optId", "store-session", "Delete an option", menuH.MeDeleteOption, storeSession),
		// Store status (main 3.0).
		def("GET", storePrefix+"/me/status", "store-session", "The store status channels see (open/paused/closed, types, sold out)", statusH.MeStatus, storeSession),
		def("GET", storePrefix+"/me/ordering", "store-session", "Ordering settings (types, ETAs, scheduling, pause) + hours exceptions", statusH.MeOrdering, storeSession),
		def("PUT", storePrefix+"/me/ordering", "store-session", "Set order types, ETAs and scheduling days", statusH.MeUpdateOrdering, storeSession),
		def("POST", storePrefix+"/me/pause", "store-session", "Pause orders {preset: 15m|30m|60m|today|indefinite, note}", statusH.MePause, storeSession),
		def("DELETE", storePrefix+"/me/pause", "store-session", "Take orders again", statusH.MeResume, storeSession),
		def("POST", storePrefix+"/me/hours-exceptions", "store-session", "Set a date closed or its own hours {date, closed, hours, note}", statusH.MeSetException, storeSession),
		def("DELETE", storePrefix+"/me/hours-exceptions/:id", "store-session", "Remove a date exception", statusH.MeDeleteException, storeSession),
		// Pricing + deals (main 3.0).
		def("GET", storePrefix+"/me/staff", "store-session", "The counter terminal's staff (never the PIN)", staffH.MeList, storeSession),
		def("POST", storePrefix+"/me/staff", "store-session", "Add a person {first_name, last_name, role, pin, limits?}", staffH.MeCreate, storeSession),
		def("GET", storePrefix+"/me/staff/journal", "store-session", "The terminal journal, newest first (?before=&limit=)", staffH.MeJournal, storeSession),
		def("PATCH", storePrefix+"/me/staff/:id", "store-session", "Change a person {first_name, last_name, role, limits, active, pin}", staffH.MeUpdate, storeSession),
		def("POST", storePrefix+"/me/staff/:id/pin", "store-session", "Reset a person's PIN {pin}", staffH.MeResetPin, storeSession),
		def("DELETE", storePrefix+"/me/staff/:id", "store-session", "Delete a person", staffH.MeDelete, storeSession),
		def("GET", storePrefix+"/me/terminal/settings", "store-session", "The store's terminal settings (role limits, auto-lock, counter start, receipt auto-Done)", staffH.MeSettings, storeSession),
		def("PUT", storePrefix+"/me/terminal/settings", "store-session", "Replace the terminal settings", staffH.MeSaveSettings, storeSession),
		def("GET", storePrefix+"/me/pricing", "store-session", "Pricing settings: the store's + each channel kind's sets", pricingH.MePricing, storeSession),
		def("PUT", storePrefix+"/me/pricing", "store-session", "Replace the pricing settings {store, kinds}", pricingH.MeSavePricing, storeSession),
		def("GET", storePrefix+"/me/pricing/context", "store-session", "What a channel kind prices with (?kind=web) — for previews", pricingH.MeContext, storeSession),
		def("GET", storePrefix+"/me/deals", "store-session", "The store's deals", pricingH.MeDeals, storeSession),
		def("POST", storePrefix+"/me/deals", "store-session", "Add a deal (bundle, mix, nth, code, free_item)", pricingH.MeCreateDeal, storeSession),
		def("PUT", storePrefix+"/me/deals/:id", "store-session", "Replace a deal", pricingH.MeUpdateDeal, storeSession),
		def("DELETE", storePrefix+"/me/deals/:id", "store-session", "Delete a deal", pricingH.MeDeleteDeal, storeSession),
		// The menu bank + views (main 3.0).
		def("GET", storePrefix+"/me/menu/bank", "store-session", "The whole menu bank (sizes, items with sizes + groups, options with prices)", viewH.MeBank, storeSession),
		def("GET", storePrefix+"/me/menu/bank/document", "store-session", "The bank, views and deals as one document (names, no ids)", bankDocH.MeExport, storeSession),
		def("POST", storePrefix+"/me/menu/bank/apply", "store-session", "Apply a bank document in one transaction (?dry_run=1 reports + rolls back, ?prune=1 deletes what it doesn't name)", bankDocH.MeApply, storeSession),
		def("GET", storePrefix+"/me/menu/sizes", "store-session", "List the store's sizes", viewH.MeListSizes, storeSession),
		def("POST", storePrefix+"/me/menu/sizes", "store-session", "Add a size {name, short}", viewH.MeCreateSize, storeSession),
		def("POST", storePrefix+"/me/menu/sizes/reorder", "store-session", "Reorder sizes {ids}", viewH.MeReorderSizes, storeSession),
		def("PATCH", storePrefix+"/me/menu/sizes/:id", "store-session", "Rename a size", viewH.MeUpdateSize, storeSession),
		def("DELETE", storePrefix+"/me/menu/sizes/:id", "store-session", "Delete a size (its item/option prices go with it)", viewH.MeDeleteSize, storeSession),
		def("PUT", storePrefix+"/me/menu/items/:id/bank", "store-session", "Set an item's kitchen name, SKU and sizes", viewH.MeSetItemBank, storeSession),
		def("PUT", storePrefix+"/me/menu/modifier-groups/:id/bank", "store-session", "Set a group's halves flag and its options' defaults, kitchen names, size prices", viewH.MeSetGroupBank, storeSession),
		def("GET", storePrefix+"/me/menu/views", "store-session", "List the menu views (defaults created on first use)", viewH.MeListViews, storeSession),
		def("POST", storePrefix+"/me/menu/views", "store-session", "Add a view {kind, name, rules}", viewH.MeCreateView, storeSession),
		def("GET", storePrefix+"/me/menu/views/:id", "store-session", "One view", viewH.MeGetView, storeSession),
		def("PATCH", storePrefix+"/me/menu/views/:id", "store-session", "Rename a view / set its rules", viewH.MeUpdateView, storeSession),
		def("DELETE", storePrefix+"/me/menu/views/:id", "store-session", "Delete a view", viewH.MeDeleteView, storeSession),
		def("GET", storePrefix+"/me/menu/views/:id/build", "store-session", "A view as its channels see it + needs attention", viewH.MeBuildView, storeSession),
		def("GET", storePrefix+"/me/menu/views/:id/layout", "store-session", "A counter view's layout (tabs, screens, flows, functions)", viewH.MeGetLayout, storeSession),
		def("PUT", storePrefix+"/me/menu/views/:id/layout", "store-session", "Replace a counter view's layout (422 lists every problem)", viewH.MePutLayout, storeSession),
		def("DELETE", storePrefix+"/me/menu/views/:id/layout", "store-session", "Clear a counter view's layout", viewH.MeDeleteLayout, storeSession),
		def("GET", storePrefix+"/me/menu/views/:id/entries", "store-session", "A view's entries", viewH.MeListEntries, storeSession),
		def("POST", storePrefix+"/me/menu/views/:id/entries", "store-session", "Add an entry (item, merge, augment, map)", viewH.MeCreateEntry, storeSession),
		def("PUT", storePrefix+"/me/menu/entries/:id", "store-session", "Replace an entry", viewH.MeUpdateEntry, storeSession),
		def("DELETE", storePrefix+"/me/menu/entries/:id", "store-session", "Delete an entry", viewH.MeDeleteEntry, storeSession),
		def("GET", storePrefix+"/me/square", "store-session", "Square: connection, locations, holds open", squareH.MeStatus, storeSession),
		def("POST", storePrefix+"/me/square/connect", "store-session", "Square: the link to connect the account → {url}", squareH.MeConnect, storeSession),
		def("PUT", storePrefix+"/me/square/location", "store-session", "Square: the location payments go to {square_location_id}", squareH.MeSetLocation, storeSession),
		def("DELETE", storePrefix+"/me/square", "store-session", "Square: disconnect (refused while holds are open)", squareH.MeDisconnect, storeSession),
		def("GET", storePrefix+"/me/card-holds", "store-session", "Recent online card holds with who ordered (?status=&limit=)", squareH.MeHolds, storeSession),
		def("POST", storePrefix+"/me/card-holds/:order_id/capture", "store-session", "Charge a waiting card order now", squareH.MeChargeNow, storeSession),
		def("GET", storePrefix+"/me/site/contact", "store-session", "The store's email for receipt replies", accountH.MeContact, storeSession),
		def("PUT", storePrefix+"/me/site/contact", "store-session", "Set the receipt reply-to email {reply_email}", accountH.MePutContact, storeSession),
		def("GET", storePrefix+"/me/site/domains", "store-session", "The website's hosts", siteH.MeDomains, storeSession),
		def("POST", storePrefix+"/me/site/domains", "store-session", "Add the store's own domain {host, primary} (unverified until checked)", siteH.MeAddDomain, storeSession),
		def("POST", storePrefix+"/me/site/domains/:host/primary", "store-session", "Make a host the primary one", siteH.MeSetPrimary, storeSession),
		def("DELETE", storePrefix+"/me/site/domains/:host", "store-session", "Remove a host", siteH.MeRemoveDomain, storeSession),
		def("GET", storePrefix+"/me/site/website", "store-session", "The Website page: draft, published, changes, preview link", siteH.MeWebsite, storeSession),
		def("PUT", storePrefix+"/me/site/website/draft", "store-session", "Save the website draft (the whole settings document)", siteH.MeSaveDraft, storeSession),
		def("DELETE", storePrefix+"/me/site/website/draft", "store-session", "Discard the website draft", siteH.MeDiscardDraft, storeSession),
		def("POST", storePrefix+"/me/site/website/publish", "store-session", "Publish the website draft", siteH.MePublish, storeSession),
		def("POST", storePrefix+"/me/site/images", "store-session", "Upload a logo or cover photo (?kind=logo|cover, raw body ≤ 2 MB)", siteH.MeAddImage, storeSession),
		def("GET", storePrefix+"/me/site/images/:id", "store-session", "A logo or cover photo", siteH.MeImage, storeSession),
		def("POST", storePrefix+"/me/menu/vendor/maps", "store-session", "Map a vendor item name to a bank item {vendor_name, item_id}", viewH.MeVendorMap, storeSession),

		// Operators (call-center order-takers) — read-only menu via bearer token.
		def("POST", storePrefix+"/operator/login", "none", "Operator login → read-only bearer token", operatorH.Login),
		def("GET", storePrefix+"/operator/me", "operator", "Operator whoami + allowed locations", operatorH.Me, operatorAuth),
		def("GET", storePrefix+"/operator/menu", "operator", "Full menu for an allowed location (POS read)", operatorH.Menu, operatorAuth),

		// /channel/v1 — the ports every sales channel talks to (main 3.0).
		def("GET", channelPrefix+"/openapi.json", "none", "The channel API's OpenAPI document", channelH.Spec, channelGzip),
		def("POST", channelPrefix+"/operator/login", "none", "An operator signs in (12 h): then Bearer + X-Location-ID on /channel/v1", operatorH.ChannelLogin, channelGzip),
		def("POST", channelPrefix+"/operator/logout", "none", "An operator signs out (Bearer the sign-in)", operatorH.ChannelLogout),
		def("GET", channelPrefix+"/me", "channel", "The channel behind the key and its store", channelH.Me, channelAuth, channelGzip),
		def("GET", channelPrefix+"/menu", "channel", "The channel's menu (ETag = menu version)", viewH.ChannelMenu, channelAuth, channelGzip),
		def("POST", channelPrefix+"/orders", "channel", "Place an order (orders permission; idempotent on client_order_id; 202 pending when main is still at work)", orderPortH.Place, channelAuth, guest, channelGzip),
		// Website customer accounts (weborder stage 2): X-Customer-Session from /account/verify.
		def("POST", channelPrefix+"/account/code", "channel", "Text (or call) a sign-in code {phone, channel sms|call}", accountH.SendCode, channelAuth, channelGzip),
		def("POST", channelPrefix+"/account/verify", "channel", "Check the code → a customer session {phone, code, name?}", accountH.Verify, channelAuth, channelGzip),
		def("GET", channelPrefix+"/account", "channel+customer", "The signed-in customer: profile, addresses, saved cards", accountH.Get, channelAuth, member, channelGzip),
		def("PATCH", channelPrefix+"/account", "channel+customer", "Set name / email", accountH.Update, channelAuth, member, channelGzip),
		def("DELETE", channelPrefix+"/account", "channel+customer", "Delete the account (saved cards removed from Square; orders stay)", accountH.Delete, channelAuth, member),
		def("POST", channelPrefix+"/account/logout", "channel+customer", "End this session", accountH.Logout, channelAuth, member),
		def("POST", channelPrefix+"/account/addresses", "channel+customer", "Save an address", accountH.AddAddress, channelAuth, member, channelGzip),
		def("PUT", channelPrefix+"/account/addresses/:id", "channel+customer", "Replace an address", accountH.PutAddress, channelAuth, member, channelGzip),
		def("DELETE", channelPrefix+"/account/addresses/:id", "channel+customer", "Delete an address", accountH.DeleteAddress, channelAuth, member),
		def("DELETE", channelPrefix+"/account/cards/:id", "channel+customer", "Forget a saved card", accountH.DeleteCard, channelAuth, member),
		def("GET", channelPrefix+"/account/orders", "channel+customer", "The account's orders, newest first (?before=&limit=)", accountH.Orders, channelAuth, member, channelGzip),
		def("POST", channelPrefix+"/account/orders/:id/reorder", "channel+customer", "A past order as a new cart, priced today {type?}", accountH.Reorder, channelAuth, member, channelGzip),
		def("GET", channelPrefix+"/orders", "channel", "This channel's orders, newest first (?before=&limit=)", orderPortH.List, channelAuth, channelGzip),
		def("GET", channelPrefix+"/orders/by-key/:key", "channel", "A placement by its client_order_id: {state pending|placed|refused|expired|voided, order?, error?, expires_at} | 404 unknown_key", orderPortH.ByKey, channelAuth, channelGzip),
		def("GET", channelPrefix+"/orders/:id", "channel", "One of this channel's orders", orderPortH.Get, channelAuth, channelGzip),
		def("GET", channelPrefix+"/orders/:id/status", "channel", "One of this channel's orders: its tracking state (track permission)", trackingH.Status, channelAuth, channelGzip),
		// No gzip on the stream: the compressor would hold events back.
		def("GET", channelPrefix+"/events", "channel", "This channel's orders as they change (SSE, Last-Event-ID resumes 24 h)", trackingH.Events, channelAuth),
		def("GET", "/track/v1/:token", "none", "A customer's tracking link: status, kitchen #, ready-by, driver first name", trackingH.Public),
		def("POST", channelPrefix+"/quote", "channel", "Price a cart (quote permission)", pricingH.ChannelQuote, channelAuth, channelGzip),
		def("GET", channelPrefix+"/pricing", "channel", "What this channel prices with (settings, deals, item facts) — for previews", pricingH.ChannelPricing, channelAuth, channelGzip),
		def("GET", channelPrefix+"/store", "channel", "The store's status: open/paused/closed, order types + ETAs, sold out", statusH.ChannelStore, channelAuth, channelGzip),
		def("GET", channelPrefix+"/store/times", "channel", "Times an order can be promised for (?type=pickup|delivery&date=YYYY-MM-DD)", statusH.ChannelTimes, channelAuth, channelGzip),
		def("POST", channelPrefix+"/store/delivery-check", "channel", "Does the store deliver to this address {line1, city, state, zip}", statusH.ChannelDeliveryCheck, channelAuth, channelGzip),
		def("GET", channelPrefix+"/payments", "channel", "How this channel takes payments (methods + the card SDK's setup)", squareH.ChannelPayments, channelAuth, channelGzip),
		def("GET", channelPrefix+"/site", "channel", "The store's website: published look + texts, hosts (ETag; ?preview=token → the draft)", siteH.ChannelSite, channelAuth, channelGzip),
		def("GET", channelPrefix+"/site/images/:id", "channel", "A logo or cover photo of the store's website", siteH.ChannelImage, channelAuth),
		def("GET", channelPrefix+"/menu/version", "channel", "The menu version (?since=N → changed)", viewH.ChannelMenuVersion, channelAuth, channelGzip),

		def("GET", storeUIPath, "none", "Redirect → /app/store", redirectTo("/app/store")),
		def("GET", storeUIPath+"/", "none", "Redirect → /app/store", redirectTo("/app/store")),
	}
}

func redirectTo(target string) echo.HandlerFunc {
	return func(c *echo.Context) error { return c.Redirect(http.StatusFound, target) }
}

// serveApp serves the embedded SvelteKit SPA under /app. Assets stream with the
// right Content-Type + long immutable cache (hashed names); an extensionless
// path that doesn't map to a file falls back to index.html (client-side
// routing). API paths never reach here — they match their own routes first.
func serveApp(c *echo.Context, fsys fs.FS, built bool) error {
	if !built {
		return c.String(http.StatusServiceUnavailable,
			"UI not built — run `task ui-build`, or use the Vite dev server (`task ui-dev`).")
	}
	p := strings.TrimPrefix(c.Request().URL.Path, "/app")
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		p = "index.html"
	}

	body, ctype, ok := readAppFile(fsys, p)
	if !ok {
		if path.Ext(p) != "" { // a missing hashed asset is a real 404
			return echo.ErrNotFound
		}
		if body, ctype, ok = readAppFile(fsys, "index.html"); !ok {
			return echo.ErrNotFound
		}
	}

	h := c.Response().Header()
	if strings.HasPrefix(p, "_app/immutable/") {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	return c.Blob(http.StatusOK, ctype, body)
}

func readAppFile(fsys fs.FS, name string) (body []byte, ctype string, ok bool) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, "", false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return nil, "", false
	}
	body, err = io.ReadAll(f)
	if err != nil {
		return nil, "", false
	}
	return body, appContentType(name), true
}

func appContentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".webmanifest":
		return "application/manifest+json"
	default:
		if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
			return ct
		}
		return "application/octet-stream"
	}
}
