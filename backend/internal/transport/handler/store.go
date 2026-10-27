package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	tenancyApp "quiccpos/main/internal/app/tenancy"
	"quiccpos/main/internal/domain/tenancy"
	appMiddleware "quiccpos/main/internal/transport/middleware"
	"quiccpos/main/internal/transport/session"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// StoreHandler owns the /store/v1/* surface — the store-manager view of the
// world. There are two authenticated entry points:
//
//   - Browser managers log in with username/password (POST /store/v1/session),
//     get a signed httpOnly session cookie scoped to their (tenant, location),
//     and use the /store/v1/me/* endpoints — scope always comes from the cookie,
//     never a URL id, so a manager can only ever touch their own store.
//   - The agent authenticates with X-STORE-API-KEY (an admin-minted device token
//     pasted into its setup screen) and uses GET/POST /store/v1/store-config +
//     POST /store/v1/agent/revoke. No username/password login path anymore.
//
// Scope split vs. /admin: /admin/v1 is dev god-mode (create tenants/locations,
// mint agent devices — handler/admin.go); /store/v1 is the single-store manager.
type StoreHandler struct {
	svc    *tenancyApp.Service
	signer *session.Signer
	cfgPub *StoreConfigPublisher // live store_config push after location/printer edits
	logins *loginLimiter
	logger zerolog.Logger
	// geocodeStore geocodes a saved location's address in the background
	// (no-op on a manual pin); nil = off. See OnLocationSaved.
	geocodeStore func(locationID uuid.UUID)
}

// OnLocationSaved registers the background store geocode run after a
// location edit (the geo service; wired in routes).
func (h *StoreHandler) OnLocationSaved(fn func(locationID uuid.UUID)) { h.geocodeStore = fn }

func NewStoreHandler(svc *tenancyApp.Service, signer *session.Signer, cfgPub *StoreConfigPublisher, logger zerolog.Logger) *StoreHandler {
	return &StoreHandler{
		svc:    svc,
		signer: signer,
		cfgPub: cfgPub,
		logins: newLoginLimiter(),
		logger: logger.With().Str("module", "store-handler").Logger(),
	}
}

// meScope pulls the (tenant, location) the current manager session is clamped
// to. ok is false only if StoreSessionAuth didn't run (never, behind the mw).
func meScope(c *echo.Context) (tenantID, locationID uuid.UUID, ok bool) {
	scope, ok := appMiddleware.StoreScopeFromContext(c.Request().Context())
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	return uuid.UUID(scope.TenantID), uuid.UUID(scope.LocationID), true
}

// --- Session (browser manager login) -------------------------------------

// POST /store/v1/session — validate username/password, set the scoped session
// cookie. This is the browser manager's login; it never mints a device key
// (agents are provisioned by an admin-minted token).
func (h *StoreHandler) SessionLogin(c *echo.Context) error {
	ctx := c.Request().Context()
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	if stop, err := h.logins.throttled(c, req.Username); stop {
		return err
	}
	tenant, err := h.svc.AuthenticateTenant(ctx, req.Username, req.Password)
	if err != nil {
		if errors.Is(err, tenancyApp.ErrInvalidCredentials) {
			h.logins.failed(req.Username)
			return c.JSON(http.StatusUnauthorized, errResp("invalid username or password"))
		}
		h.logger.Error().Err(err).Msg("session login failed")
		return c.JSON(http.StatusInternalServerError, errResp("login failed"))
	}
	h.logins.succeeded(req.Username)
	loc, err := h.svc.PrimaryLocation(ctx, tenant.ID)
	if err != nil {
		if errors.Is(err, tenancyApp.ErrNoPrimaryLocation) {
			// Can't happen for tenants created since migration 00003 (the
			// location is created with the tenant); a clear 409, not a 500.
			return c.JSON(http.StatusConflict, errResp("this store has no location yet — ask the admin to finish setting it up"))
		}
		h.logger.Error().Err(err).Msg("session login: location lookup failed")
		return c.JSON(http.StatusInternalServerError, errResp("login failed"))
	}
	h.signer.Issue(c.Response(), c.Request(), tenant.ID, loc.ID)
	return c.JSON(http.StatusOK, map[string]any{
		"tenant_id":   tenant.ID.String(),
		"location_id": loc.ID.String(),
		"tenant_name": tenant.Name,
	})
}

// DELETE /store/v1/session — clear the session cookie.
func (h *StoreHandler) SessionLogout(c *echo.Context) error {
	h.signer.Clear(c.Response(), c.Request())
	return c.NoContent(http.StatusNoContent)
}

// GET /store/v1/session — whoami. Behind StoreSessionAuth; returns the
// manager's tenant + location so the SPA can render without a second call, and
// whether this is an admin's open-as-store session (impersonated[_by]).
func (h *StoreHandler) SessionWhoami(c *echo.Context) error {
	ctx := c.Request().Context()
	tid, lid, ok := meScope(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, errResp("login required"))
	}
	t, err := h.svc.GetTenant(ctx, tid)
	if err != nil {
		// Cookie is validly signed but its tenant/location no longer exist (e.g.
		// a dev DB reset wiped them). Log the manager out gracefully — a 401 lets
		// the SPA fall back to the login screen instead of hard-failing the page.
		if errors.Is(err, pgx.ErrNoRows) {
			h.signer.Clear(c.Response(), c.Request())
			return c.JSON(http.StatusUnauthorized, errResp("session expired — please log in again"))
		}
		return c.JSON(http.StatusInternalServerError, errResp("tenant lookup failed"))
	}
	l, err := h.svc.GetLocation(ctx, lid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.signer.Clear(c.Response(), c.Request())
			return c.JSON(http.StatusUnauthorized, errResp("session expired — please log in again"))
		}
		return c.JSON(http.StatusInternalServerError, errResp("location lookup failed"))
	}
	resp := map[string]any{
		"tenant":       toTenantJSON(*t),
		"location":     toLocationJSON(*l),
		"impersonated": false,
	}
	if by := appMiddleware.StoreImpersonatorFromContext(ctx); by != "" {
		resp["impersonated"] = true
		resp["impersonated_by"] = by
	}
	return c.JSON(http.StatusOK, resp)
}

// --- /me/* — session-scoped self-service (location_id from the cookie) ----

// GET /store/v1/me/location
func (h *StoreHandler) MeGetLocation(c *echo.Context) error {
	ctx := c.Request().Context()
	_, lid, _ := meScope(c)
	l, err := h.svc.GetLocation(ctx, lid)
	if err != nil {
		return c.JSON(http.StatusNotFound, errResp("location not found"))
	}
	return c.JSON(http.StatusOK, toLocationJSON(*l))
}

// PATCH /store/v1/me/location — full-shape update of the manager's own location.
func (h *StoreHandler) MeUpdateLocation(c *echo.Context) error {
	ctx := c.Request().Context()
	tid, lid, _ := meScope(c)
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
	l, err := h.svc.UpdateLocation(ctx, tenancy.UpdateLocationParams{
		ID:             lid,
		Name:           req.Name,
		Timezone:       req.Timezone,
		DailyResetHour: req.DailyResetHour,
		Phone:          req.Phone,
		AddressLine1:   req.AddressLine1,
		AddressLine2:   req.AddressLine2,
		Config:         req.Config,
	})
	if err != nil {
		return c.JSON(http.StatusBadRequest, locationErr(err))
	}
	h.cfgPub.Publish(ctx, tid, lid)
	if h.geocodeStore != nil {
		h.geocodeStore(lid)
	}
	return c.JSON(http.StatusOK, toLocationJSON(*l))
}

// GET /store/v1/me/printers
func (h *StoreHandler) MeListPrinters(c *echo.Context) error {
	ctx := c.Request().Context()
	_, lid, _ := meScope(c)
	rows, err := h.svc.ListPrintersByLocation(ctx, lid)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("list failed"))
	}
	out := make([]printerJSON, 0, len(rows))
	for _, p := range rows {
		out = append(out, toPrinterJSON(p))
	}
	return c.JSON(http.StatusOK, out)
}

// POST /store/v1/me/printers {name, ip, mac?} — mac when the manager adds a
// printer the Pi found on the network (the Pi follows it by MAC).
func (h *StoreHandler) MeCreatePrinter(c *echo.Context) error {
	ctx := c.Request().Context()
	tid, lid, _ := meScope(c)
	var req struct {
		Name string `json:"name"`
		IP   string `json:"ip"`
		MAC  string `json:"mac"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	p, err := h.svc.AddPrinter(ctx, lid, req.Name, req.IP, req.MAC)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	h.cfgPub.Publish(ctx, tid, lid)
	return c.JSON(http.StatusCreated, toPrinterJSON(*p))
}

// PATCH /store/v1/me/printers/:id — only if the printer belongs to this location.
func (h *StoreHandler) MeUpdatePrinter(c *echo.Context) error {
	ctx := c.Request().Context()
	tid, lid, _ := meScope(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	if owned, err := h.ownsPrinter(ctx, lid, id); err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("lookup failed"))
	} else if !owned {
		return c.JSON(http.StatusNotFound, errResp("printer not found"))
	}
	var req struct {
		Name string `json:"name"`
		IP   string `json:"ip"`
		// IsReceipt=true moves the location's receipt-printer flag here. The
		// flag can only move (false is ignored) — a location with printers
		// always has exactly one receipt printer.
		IsReceipt *bool `json:"is_receipt"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	p, err := h.svc.UpdatePrinter(ctx, id, req.Name, req.IP)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	if req.IsReceipt != nil && *req.IsReceipt {
		if p, err = h.svc.SetReceiptPrinter(ctx, lid, id); err != nil {
			return c.JSON(http.StatusInternalServerError, errResp(err.Error()))
		}
	}
	h.cfgPub.Publish(ctx, tid, lid)
	return c.JSON(http.StatusOK, toPrinterJSON(*p))
}

// DELETE /store/v1/me/printers/:id
func (h *StoreHandler) MeDeletePrinter(c *echo.Context) error {
	ctx := c.Request().Context()
	tid, lid, _ := meScope(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	if owned, err := h.ownsPrinter(ctx, lid, id); err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("lookup failed"))
	} else if !owned {
		return c.JSON(http.StatusNotFound, errResp("printer not found"))
	}
	if err := h.svc.DeletePrinter(ctx, id); err != nil {
		return c.JSON(http.StatusInternalServerError, errResp(err.Error()))
	}
	h.cfgPub.Publish(ctx, tid, lid)
	return c.NoContent(http.StatusNoContent)
}

// GET /store/v1/me/printers/ip-changes?limit=20 — this location's printer IP
// changes, newest first: the Pi following a printer to a new IP (source
// "agent") and manager edits (source "manager").
func (h *StoreHandler) MeListPrinterIPChanges(c *echo.Context) error {
	ctx := c.Request().Context()
	_, lid, _ := meScope(c)
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	rows, err := h.svc.ListPrinterIPChanges(ctx, lid, limit)
	if err != nil {
		h.logger.Error().Err(err).Msg("list printer ip changes failed")
		return c.JSON(http.StatusInternalServerError, errResp("list failed"))
	}
	type changeJSON struct {
		ID          string    `json:"id"`
		PrinterID   string    `json:"printer_id"`
		PrinterName string    `json:"printer_name"`
		OldIP       string    `json:"old_ip"`
		NewIP       string    `json:"new_ip"`
		MAC         string    `json:"mac"`
		Source      string    `json:"source"`
		At          time.Time `json:"at"`
	}
	out := make([]changeJSON, 0, len(rows))
	for _, r := range rows {
		out = append(out, changeJSON{
			ID: r.ID.String(), PrinterID: r.PrinterID.String(), PrinterName: r.PrinterName,
			OldIP: r.OldIP, NewIP: r.NewIP, MAC: r.MAC, Source: r.Source, At: r.At,
		})
	}
	return c.JSON(http.StatusOK, out)
}

// GET /store/v1/me/devices
func (h *StoreHandler) MeListDevices(c *echo.Context) error {
	ctx := c.Request().Context()
	_, lid, _ := meScope(c)
	rows, err := h.svc.ListDevicesByLocation(ctx, lid)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("list failed"))
	}
	out := make([]deviceJSON, 0, len(rows))
	for _, d := range rows {
		out = append(out, toDeviceJSON(d))
	}
	return c.JSON(http.StatusOK, out)
}

// POST /store/v1/me/devices — mint a NON-agent device for the manager's location.
func (h *StoreHandler) MeMintDevice(c *echo.Context) error {
	ctx := c.Request().Context()
	_, lid, _ := meScope(c)
	var req struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	if err := validateNonAgentKind(req.Kind); err != nil {
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	minted, err := h.svc.MintDevice(ctx, lid, req.Name, req.Kind)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	return c.JSON(http.StatusCreated, struct {
		Device deviceJSON `json:"device"`
		Key    string     `json:"api_key"`
	}{Device: toDeviceJSON(minted.Device), Key: minted.ClearKey})
}

// POST /store/v1/me/devices/:id/revoke — only if the device is in this location.
func (h *StoreHandler) MeRevokeDevice(c *echo.Context) error {
	ctx := c.Request().Context()
	_, lid, _ := meScope(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	if owned, err := h.ownsDevice(ctx, lid, id); err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("lookup failed"))
	} else if !owned {
		return c.JSON(http.StatusNotFound, errResp("device not found"))
	}
	if err := h.svc.RevokeDevice(ctx, id); err != nil {
		return c.JSON(http.StatusInternalServerError, errResp(err.Error()))
	}
	return c.NoContent(http.StatusNoContent)
}

// ownsPrinter / ownsDevice enforce that a :id under /me/* belongs to the
// session's location (scope from the cookie, never the URL) using the already
// scope-filtered list queries — no extra service method needed.
func (h *StoreHandler) ownsPrinter(ctx context.Context, locationID, printerID uuid.UUID) (bool, error) {
	ps, err := h.svc.ListPrintersByLocation(ctx, locationID)
	if err != nil {
		return false, err
	}
	for _, p := range ps {
		if p.ID == printerID {
			return true, nil
		}
	}
	return false, nil
}

func (h *StoreHandler) ownsDevice(ctx context.Context, locationID, deviceID uuid.UUID) (bool, error) {
	ds, err := h.svc.ListDevicesByLocation(ctx, locationID)
	if err != nil {
		return false, err
	}
	for _, d := range ds {
		if d.ID == deviceID {
			return true, nil
		}
	}
	return false, nil
}

// --- Agent surface: device-keyed store config + self-revoke ---------------

// POST /store/v1/agent/revoke — device-authed self-revoke. The agent calls this
// (with its own X-STORE-API-KEY) from its Settings → "Revoke my token" flow; we
// revoke the calling device so the key stops working immediately.
func (h *StoreHandler) AgentSelfRevoke(c *echo.Context) error {
	ctx := c.Request().Context()
	dev := appMiddleware.DeviceFromContext(ctx)
	if dev == nil {
		return c.JSON(http.StatusUnauthorized, errResp("unauthorized"))
	}
	if err := h.svc.RevokeDevice(ctx, dev.ID); err != nil {
		h.logger.Error().Err(err).Str("device_id", dev.ID.String()).Msg("agent self-revoke failed")
		return c.JSON(http.StatusInternalServerError, errCode("Couldn't revoke this device.", "revoke_failed"))
	}
	return c.NoContent(http.StatusNoContent)
}

// storeConfigJSON is the editable store-config surface the agent web UI renders.
type storeConfigJSON struct {
	Location locationJSON  `json:"location"`
	Printers []printerJSON `json:"printers"`
}

// GET /store/v1/store-config — the caller's current config, scoped by the store
// API key (not a URL id). The agent's setup screen calls it to verify a pasted
// token, and its (read-only) Store-config tab displays it. Edits happen only in
// /app/store (session routes), which push a fresh store_config frame live.
func (h *StoreHandler) GetStoreConfig(c *echo.Context) error {
	ctx := c.Request().Context()
	scope := appMiddleware.ScopeFromContext(ctx)
	locID := uuid.UUID(scope.LocationID)

	loc, err := h.svc.GetLocation(ctx, locID)
	if err != nil {
		return c.JSON(http.StatusNotFound, errResp("location not found"))
	}
	printers, err := h.svc.ListPrintersByLocation(ctx, locID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("list printers failed"))
	}
	out := storeConfigJSON{Location: toLocationJSON(*loc), Printers: make([]printerJSON, 0, len(printers))}
	for _, p := range printers {
		out.Printers = append(out.Printers, toPrinterJSON(p))
	}
	return c.JSON(http.StatusOK, out)
}

// validateNonAgentKind enforces the /store-side device-mint policy. `agent`
// kind has to come from /admin so we keep the agent provisioning flow
// (dev-only) separate from the manager-level provisioning flow.
func validateNonAgentKind(kind string) error {
	switch kind {
	case tenancyApp.DeviceKindMobile,
		tenancyApp.DeviceKindKiosk,
		tenancyApp.DeviceKindIngest:
		return nil
	case "", tenancyApp.DeviceKindAgent:
		return errors.New("kind must be one of: mobile, kiosk, ingest (agents are minted via /admin)")
	default:
		return errors.New("unknown device kind: " + kind)
	}
}
