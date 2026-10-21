package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	orderApp "quiccpos/main/internal/app/order"
	tenancyApp "quiccpos/main/internal/app/tenancy"
	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/transport/session"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// AdminHandler owns the /admin/v1/* surface. Every route is admin-gated
// upstream (see middleware.AdminAuth). Responses use a flat JSON shape that
// the dev /admin index.html consumes directly with fetch().
//
// Admin scope vs. /store scope:
//   - /admin/v1 = dev-only god mode. Creates tenants, locations. Mints
//     `agent` devices. Edits tenant/location config.
//   - /store/v1 = store-manager surface. Edits one location's config,
//     prints, and non-agent devices. Lives in handler/store.go.
type AdminHandler struct {
	svc      *tenancyApp.Service
	orderSvc *orderApp.Service
	cfgPub   *StoreConfigPublisher // live store_config push after location edits
	signer   *session.Signer       // open-as-store: impersonated store session cookie
	logger   zerolog.Logger
}

func NewAdminHandler(svc *tenancyApp.Service, orderSvc *orderApp.Service, cfgPub *StoreConfigPublisher, signer *session.Signer, logger zerolog.Logger) *AdminHandler {
	return &AdminHandler{
		svc:      svc,
		orderSvc: orderSvc,
		cfgPub:   cfgPub,
		signer:   signer,
		logger:   logger.With().Str("module", "admin-handler").Logger(),
	}
}

// --- JSON shapes -----------------------------------------------------------

type tenantJSON struct {
	ID        string         `json:"id"`
	Slug      string         `json:"slug"`
	Name      string         `json:"name"`
	Config    map[string]any `json:"config"`
	Username  string         `json:"username"`
	HasLogin  bool           `json:"has_login"`
	StoreID   *int64         `json:"store_id"` // external vendor store id; null = routes by store_name only
	CreatedAt time.Time      `json:"created_at"`
}

type locationJSON struct {
	ID             string         `json:"id"`
	TenantID       string         `json:"tenant_id"`
	Slug           string         `json:"slug"`
	Name           string         `json:"name"`
	Timezone       string         `json:"timezone"`
	DailyResetHour int            `json:"daily_reset_hour"`
	Phone          string         `json:"phone"`
	AddressLine1   string         `json:"address_line1"`
	AddressLine2   string         `json:"address_line2"`
	Config         map[string]any `json:"config"`
	CreatedAt      time.Time      `json:"created_at"`
}

type deviceJSON struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenant_id"`
	LocationID string     `json:"location_id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	KeyPrefix  string     `json:"key_prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

type printerJSON struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenant_id"`
	LocationID string    `json:"location_id"`
	Name       string    `json:"name"`
	IP         string    `json:"ip"`
	IsReceipt  bool      `json:"is_receipt"` // the location's receipt printer (else a kitchen station)
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func toTenantJSON(t tenancy.Tenant) tenantJSON {
	cfg := t.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	return tenantJSON{
		ID: t.ID.String(), Slug: t.Slug, Name: t.Name, Config: cfg,
		Username: t.Username, HasLogin: t.Username != "",
		StoreID: t.StoreID, CreatedAt: t.CreatedAt,
	}
}

func toLocationJSON(l tenancy.Location) locationJSON {
	cfg := l.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	return locationJSON{
		ID: l.ID.String(), TenantID: l.TenantID.String(),
		Slug: l.Slug, Name: l.Name,
		Timezone: l.Timezone, DailyResetHour: l.DailyResetHour,
		Phone:        l.Phone,
		AddressLine1: l.AddressLine1, AddressLine2: l.AddressLine2,
		Config:    cfg,
		CreatedAt: l.CreatedAt,
	}
}

func toDeviceJSON(d tenancy.Device) deviceJSON {
	return deviceJSON{
		ID: d.ID.String(), TenantID: d.TenantID.String(), LocationID: d.LocationID.String(),
		Name: d.Name, Kind: d.Kind, KeyPrefix: d.KeyPrefix,
		CreatedAt: d.CreatedAt, LastUsedAt: d.LastUsedAt, RevokedAt: d.RevokedAt,
	}
}

func toPrinterJSON(p tenancy.Printer) printerJSON {
	return printerJSON{
		ID: p.ID.String(), TenantID: p.TenantID.String(), LocationID: p.LocationID.String(),
		Name: p.Name, IP: p.IP, IsReceipt: p.IsReceipt,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

// --- Tenants ---------------------------------------------------------------

// POST /admin/v1/tenants
func (h *AdminHandler) CreateTenant(c *echo.Context) error {
	var req struct {
		Slug     string `json:"slug"`
		Name     string `json:"name"`
		Username string `json:"username"`
		Password string `json:"password"`
		StoreID  *int64 `json:"store_id"` // optional external vendor store id
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	t, loc, err := h.svc.CreateTenant(c.Request().Context(), req.Slug, req.Name, req.Username, req.Password, req.StoreID)
	if err != nil {
		h.logger.Error().Err(err).Msg("CreateTenant failed")
		if errors.Is(err, tenancy.ErrStoreIDTaken) || errors.Is(err, tenancy.ErrUsernameTaken) {
			return c.JSON(http.StatusConflict, errResp(err.Error()))
		}
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	// One tenant = one restaurant = one location: the location is created with
	// the tenant and returned alongside it.
	return c.JSON(http.StatusCreated, struct {
		tenantJSON
		Location locationJSON `json:"location"`
	}{toTenantJSON(*t), toLocationJSON(*loc)})
}

// PATCH /admin/v1/tenants/:id/store-id — set or clear (`{"store_id": null}`)
// the tenant's external vendor store_id, the key online orders route by.
func (h *AdminHandler) SetTenantStoreID(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	var req struct {
		StoreID *int64 `json:"store_id"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	t, err := h.svc.SetTenantStoreID(c.Request().Context(), id, req.StoreID)
	switch {
	case err == nil:
		return c.JSON(http.StatusOK, toTenantJSON(*t))
	case errors.Is(err, tenancy.ErrStoreIDTaken):
		return c.JSON(http.StatusConflict, errResp(err.Error()))
	case errors.Is(err, pgx.ErrNoRows):
		return c.JSON(http.StatusNotFound, errResp("tenant not found"))
	default:
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
}

// PATCH /admin/v1/tenants/:id/credentials — set/rotate a tenant's store-manager
// login. Also the provisioning path for real store logins.
func (h *AdminHandler) SetTenantCredentials(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	if err := h.svc.SetTenantCredentials(c.Request().Context(), id, req.Username, req.Password); err != nil {
		if errors.Is(err, tenancy.ErrUsernameTaken) {
			return c.JSON(http.StatusConflict, errResp(err.Error()))
		}
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	return c.NoContent(http.StatusNoContent)
}

// impersonatorAdmin labels a session opened from the admin surface (the single
// shared ADMIN_API_KEY has no per-person identity). "admin" is also a reserved
// store username so the label can't be confused with a real manager.
const impersonatorAdmin = "admin"

// POST /admin/v1/tenants/:id/store-session — open this tenant's store as its
// manager: sets a store session cookie (session.ImpersonateTTL, stamped
// impersonated_by=admin) so the admin's browser can use /app/store without the
// manager's password. Audited at warn level; the cookie's scope is that one
// tenant, exactly like the manager's own login.
func (h *AdminHandler) OpenStoreSession(c *echo.Context) error {
	ctx := c.Request().Context()
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	t, err := h.svc.GetTenant(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errResp("tenant not found"))
		}
		h.logger.Error().Ctx(ctx).Err(err).Str("tenant_id", id.String()).Msg("open store session: tenant lookup failed")
		return c.JSON(http.StatusInternalServerError, errResp("tenant lookup failed"))
	}
	loc, err := h.svc.PrimaryLocation(ctx, t.ID)
	if err != nil {
		if errors.Is(err, tenancyApp.ErrNoPrimaryLocation) {
			return c.JSON(http.StatusConflict, errResp("this store has no location yet"))
		}
		h.logger.Error().Ctx(ctx).Err(err).Str("tenant_id", id.String()).Msg("open store session: location lookup failed")
		return c.JSON(http.StatusInternalServerError, errResp("location lookup failed"))
	}
	if err := h.signer.IssueAs(c.Response(), c.Request(), t.ID, loc.ID, impersonatorAdmin); err != nil {
		h.logger.Error().Ctx(ctx).Err(err).Msg("open store session: issue failed")
		return c.JSON(http.StatusInternalServerError, errResp("could not open store session"))
	}
	// Audit line: who/what/where, never the cookie value.
	h.logger.Warn().Ctx(ctx).
		Str("event", "admin opened store session").
		Str("tenant_id", t.ID.String()).
		Str("location_id", loc.ID.String()).
		Str("impersonated_by", impersonatorAdmin).
		Str("remote_ip", c.RealIP()).
		Msg("admin opened store session")
	return c.JSON(http.StatusOK, map[string]any{
		"tenant_id":          t.ID.String(),
		"location_id":        loc.ID.String(),
		"tenant_name":        t.Name,
		"expires_in_seconds": int(session.ImpersonateTTL.Seconds()),
	})
}

// DELETE /admin/v1/orders/:id — delete a single order + children. Used to clean
// up test/straggler orders.
func (h *AdminHandler) DeleteOrder(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	if err := h.orderSvc.DeleteByID(c.Request().Context(), order.ID(id)); err != nil {
		h.logger.Error().Err(err).Str("order_id", id.String()).Msg("DeleteOrder failed")
		return c.JSON(http.StatusInternalServerError, errResp("delete failed"))
	}
	return c.NoContent(http.StatusNoContent)
}

// orderSummaryJSON is the lightweight admin order-list shape.
type orderSummaryJSON struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	LocationID    string    `json:"location_id"`
	Source        string    `json:"source"`
	SourceEventID string    `json:"source_event_id"`
	Status        string    `json:"status"`
	CustomerName  string    `json:"customer_name"`
	TotalCents    int64     `json:"total_cents"`
	ReceivedAt    time.Time `json:"received_at"`
}

// GET /admin/v1/orders — cross-tenant order listing (admin). Optional query:
// ?source_event_id= &tenant_id= &limit= (default 200). Backs the console's
// order-test verification + cleanup enumeration.
func (h *AdminHandler) ListOrders(c *echo.Context) error {
	f := order.ListFilter{
		TenantID:      c.QueryParam("tenant_id"),
		SourceEventID: c.QueryParam("source_event_id"),
	}
	if v := c.QueryParam("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}
	rows, err := h.orderSvc.ListSummaries(c.Request().Context(), f)
	if err != nil {
		h.logger.Error().Err(err).Msg("ListOrders failed")
		return c.JSON(http.StatusInternalServerError, errResp("list failed"))
	}
	out := make([]orderSummaryJSON, 0, len(rows))
	for _, s := range rows {
		out = append(out, orderSummaryJSON{
			ID: s.ID, TenantID: s.TenantID, LocationID: s.LocationID,
			Source: s.Source, SourceEventID: s.SourceEventID, Status: s.Status,
			CustomerName: s.CustomerName, TotalCents: s.TotalCents, ReceivedAt: s.ReceivedAt,
		})
	}
	return c.JSON(http.StatusOK, out)
}

// DELETE /admin/v1/orders — bulk-delete orders (admin). Optional ?tenant_id=
// scopes to one tenant; absent deletes ALL orders. Dev cleanup. Returns
// {"deleted": N}.
func (h *AdminHandler) DeleteOrders(c *echo.Context) error {
	n, err := h.orderSvc.DeleteAll(c.Request().Context(), c.QueryParam("tenant_id"))
	if err != nil {
		h.logger.Error().Err(err).Msg("DeleteOrders (bulk) failed")
		return c.JSON(http.StatusInternalServerError, errResp("bulk delete failed"))
	}
	return c.JSON(http.StatusOK, map[string]int64{"deleted": n})
}

// GET /admin/v1/tenants
func (h *AdminHandler) ListTenants(c *echo.Context) error {
	rows, err := h.svc.ListTenants(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("list failed"))
	}
	out := make([]tenantJSON, 0, len(rows))
	for _, t := range rows {
		out = append(out, toTenantJSON(t))
	}
	return c.JSON(http.StatusOK, out)
}

// DELETE /admin/v1/tenants/:id — cascade-delete the tenant and every row
// that referenced it (locations, devices, printers, orders, outbox events).
// One transaction. Used by:
//   - the dev-time /admin UI when you want to retire a test tenant
//   - the project-level test suite's teardown so the tenants table doesn't
//     grow forever
func (h *AdminHandler) DeleteTenant(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	if err := h.svc.DeleteTenant(c.Request().Context(), id); err != nil {
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	return c.NoContent(http.StatusNoContent)
}

// PATCH /admin/v1/tenants/:id/config
func (h *AdminHandler) UpdateTenantConfig(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	// BindBody, not Bind: echo's default Bind also copies path params into the
	// target, and for a map that injected the tenant's ":id" into its config.
	var cfg map[string]any
	if err := echo.BindBody(c, &cfg); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("body must be a json object"))
	}
	t, err := h.svc.UpdateTenantConfig(c.Request().Context(), id, cfg)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	// Tenant config rides store_config: push it to the agent live.
	if loc, err := h.svc.PrimaryLocation(c.Request().Context(), id); err == nil {
		h.cfgPub.Publish(c.Request().Context(), id, loc.ID)
	}
	return c.JSON(http.StatusOK, toTenantJSON(*t))
}

// --- Locations -------------------------------------------------------------

// GET /admin/v1/locations
func (h *AdminHandler) ListLocations(c *echo.Context) error {
	rows, err := h.svc.ListLocations(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("list failed"))
	}
	out := make([]locationJSON, 0, len(rows))
	for _, l := range rows {
		out = append(out, toLocationJSON(l))
	}
	return c.JSON(http.StatusOK, out)
}

// PATCH /admin/v1/locations/:id  — full-shape update (no partial semantics).
func (h *AdminHandler) UpdateLocation(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	var req struct {
		Name           string         `json:"name"`
		Timezone       string         `json:"timezone"`
		DailyResetHour int            `json:"daily_reset_hour"`
		Phone          string         `json:"phone"`
		AddressLine1   string         `json:"address_line1"`
		AddressLine2   string         `json:"address_line2"`
		Config         map[string]any `json:"config"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	l, err := h.svc.UpdateLocation(c.Request().Context(), tenancy.UpdateLocationParams{
		ID:             id,
		Name:           req.Name,
		Timezone:       req.Timezone,
		DailyResetHour: req.DailyResetHour,
		Phone:          req.Phone,
		AddressLine1:   req.AddressLine1,
		AddressLine2:   req.AddressLine2,
		Config:         req.Config,
	})
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	h.cfgPub.Publish(c.Request().Context(), l.TenantID, l.ID)
	return c.JSON(http.StatusOK, toLocationJSON(*l))
}

// --- Devices ---------------------------------------------------------------

// POST /admin/v1/devices  — body{location_id, name}. Admin always mints
// `agent` kind; non-agent devices are minted via /store/v1/devices.
// Returns the new device + the cleartext key (ONE-TIME, never retrievable).
func (h *AdminHandler) MintAgentDevice(c *echo.Context) error {
	var req struct {
		LocationID string `json:"location_id"`
		Name       string `json:"name"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	lid, err := uuid.Parse(req.LocationID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("location_id must be a uuid"))
	}
	minted, err := h.svc.MintDevice(c.Request().Context(), lid, req.Name, tenancyApp.DeviceKindAgent)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	resp := struct {
		Device deviceJSON `json:"device"`
		Key    string     `json:"api_key"`
	}{
		Device: toDeviceJSON(minted.Device),
		Key:    minted.ClearKey,
	}
	return c.JSON(http.StatusCreated, resp)
}

// GET /admin/v1/devices
func (h *AdminHandler) ListDevices(c *echo.Context) error {
	rows, err := h.svc.ListDevices(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("list failed"))
	}
	out := make([]deviceJSON, 0, len(rows))
	for _, d := range rows {
		out = append(out, toDeviceJSON(d))
	}
	return c.JSON(http.StatusOK, out)
}

// POST /admin/v1/devices/:id/revoke
func (h *AdminHandler) RevokeDevice(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	if err := h.svc.RevokeDevice(c.Request().Context(), id); err != nil {
		return c.JSON(http.StatusInternalServerError, errResp(err.Error()))
	}
	return c.NoContent(http.StatusNoContent)
}

// DELETE /admin/v1/devices/:id — hard-delete a device row (vs revoke's tombstone).
func (h *AdminHandler) DeleteDevice(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	if err := h.svc.DeleteDevice(c.Request().Context(), id); err != nil {
		return c.JSON(http.StatusInternalServerError, errResp(err.Error()))
	}
	return c.NoContent(http.StatusNoContent)
}

// --- Overview --------------------------------------------------------------

// GET /admin/v1/overview
// One-shot dump that the dev index.html uses to render everything in a
// single fetch. Flat slices — the client groups them locally.
func (h *AdminHandler) Overview(c *echo.Context) error {
	ctx := c.Request().Context()
	tenants, err := h.svc.ListTenants(ctx)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("list tenants failed"))
	}
	locations, err := h.svc.ListLocations(ctx)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("list locations failed"))
	}
	devices, err := h.svc.ListDevices(ctx)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("list devices failed"))
	}
	out := struct {
		Tenants   []tenantJSON   `json:"tenants"`
		Locations []locationJSON `json:"locations"`
		Devices   []deviceJSON   `json:"devices"`
	}{
		Tenants:   make([]tenantJSON, 0, len(tenants)),
		Locations: make([]locationJSON, 0, len(locations)),
		Devices:   make([]deviceJSON, 0, len(devices)),
	}
	for _, t := range tenants {
		out.Tenants = append(out.Tenants, toTenantJSON(t))
	}
	for _, l := range locations {
		out.Locations = append(out.Locations, toLocationJSON(l))
	}
	for _, d := range devices {
		out.Devices = append(out.Devices, toDeviceJSON(d))
	}
	return c.JSON(http.StatusOK, out)
}
