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
	diagApp "quiccpos/main/internal/app/diag"
	menuApp "quiccpos/main/internal/app/menu"
	operatorApp "quiccpos/main/internal/app/operator"
	orderSvc "quiccpos/main/internal/app/order"
	"quiccpos/main/internal/app/printerstatus"
	tenancyApp "quiccpos/main/internal/app/tenancy"
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
	"github.com/rs/zerolog"
)

const (
	apiPrefix   = "/api/v1"
	adminPrefix = "/admin/v1"
	adminUIPath = "/admin"
	storePrefix = "/store/v1"
	storeUIPath = "/store"
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
	// AgentSync backs agent link / restore / backups / fleet health.
	AgentSync *agentsync.Service
	// Diag backs tablet diagnostics (report tokens, reports, admin view).
	Diag *diagApp.Service
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
	readiness := handler.NewReadinessHandler(d.Pool, d.SQS, d.QueueURL, d.Logger)
	appRelH := handler.NewAppReleaseHandler(d.Pool, d.Logger)
	agentLinkH := handler.NewAgentLinkHandler(d.AgentSync, d.Logger)
	diagH := handler.NewDiagHandler(d.Diag, d.Logger)
	agentUpdateH := handler.NewAgentUpdateHandler(d.AgentSync, d.Requester, d.Logger)

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
		def("POST", storePrefix+"/agent/backup", "device", "Upload the agent's nightly SQLite copy (newest 2 kept) — agent keys only", agentLinkH.Backup, device, agentOnly),
		def("POST", storePrefix+"/agent/rpc", "device", "Duplex channel: agent→main replies + reports — agent keys only", agentRPCH.Handle, device, agentOnly),
		def("GET", storePrefix+"/agent/app/latest", "device", "Newest tablet-app release (?app=quibase; 204 none) — agent keys only", appRelH.Latest, device, agentOnly),
		def("GET", storePrefix+"/agent/app/releases/:code/apk", "device", "Download a tablet-app APK (?app=quibase) — agent keys only", appRelH.APK, device, agentOnly),
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
		def("PATCH", storePrefix+"/me/location", "store-session", "Edit this manager's location", storeH.MeUpdateLocation, storeSession),
		def("GET", storePrefix+"/me/orders", "store-session", "Recent orders with the agent-synced floor status (?limit)", floorH.MeListOrders, storeSession),
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

		// Operators (call-center order-takers) — read-only menu via bearer token.
		def("POST", storePrefix+"/operator/login", "none", "Operator login → read-only bearer token", operatorH.Login),
		def("GET", storePrefix+"/operator/me", "operator", "Operator whoami + allowed locations", operatorH.Me, operatorAuth),
		def("GET", storePrefix+"/operator/menu", "operator", "Full menu for an allowed location (POS read)", operatorH.Menu, operatorAuth),

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
