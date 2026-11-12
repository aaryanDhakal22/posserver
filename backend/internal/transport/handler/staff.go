package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"net/http"
	menuApp "quiccpos/main/internal/app/menu"
	"strconv"
	"time"

	menuviewApp "quiccpos/main/internal/app/menuview"
	pricingApp "quiccpos/main/internal/app/pricing"
	staffApp "quiccpos/main/internal/app/staff"
	"quiccpos/main/internal/domain/menuview"
	"quiccpos/main/internal/domain/pricing"
	"quiccpos/main/internal/domain/staff"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// StaffHandler serves Store › Staff (the counter terminal's staff + the
// store's terminal settings) and the Pi's two terminal pulls
// (TERMINAL-PLAN.md 0d, contract/main.md §2.5zb).
type StaffHandler struct {
	svc     *staffApp.Service
	views   *menuviewApp.Service
	pricing *pricingApp.Service
	menu    *menuApp.Service // per-item kitchen printers for the counter menu
	logger  zerolog.Logger
}

func NewStaffHandler(svc *staffApp.Service, views *menuviewApp.Service, pricing *pricingApp.Service, menu *menuApp.Service, lg zerolog.Logger) *StaffHandler {
	return &StaffHandler{svc: svc, views: views, pricing: pricing, menu: menu, logger: lg.With().Str("module", "staff-handler").Logger()}
}

func (h *StaffHandler) err(c *echo.Context, err error) error {
	code := ""
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, staff.ErrBadPin):
		code = "bad_pin"
	case errors.Is(err, staff.ErrBadName):
		code = "bad_name"
	case errors.Is(err, staff.ErrBadRole):
		code = "bad_role"
	case errors.Is(err, staff.ErrBadLimits):
		code = "bad_limits"
	case errors.Is(err, staff.ErrBadSettings):
		code = "bad_settings"
	case errors.Is(err, staff.ErrPinRequired):
		code = "pin_required"
	case errors.Is(err, staff.ErrPinTaken):
		code, status = "pin_taken", http.StatusConflict
	case errors.Is(err, staff.ErrTooMany):
		code, status = "too_many", http.StatusConflict
	case errors.Is(err, staff.ErrNotFound):
		code, status = "not_found", http.StatusNotFound
	default:
		h.logger.Error().Err(err).Msg("staff")
		return c.JSON(http.StatusInternalServerError, errCode("staff failed", "internal"))
	}
	return c.JSON(status, errCode(err.Error(), code))
}

// decodeStrict reads a JSON body, refusing unknown fields.
func decodeStrict(c *echo.Context, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 64<<10))
	dec.DisallowUnknownFields()
	return dec.Decode(v) == nil
}

// GET /store/v1/me/staff
func (h *StaffHandler) MeList(c *echo.Context) error {
	_, lid, _ := meScope(c)
	out, err := h.svc.List(c.Request().Context(), lid)
	if err != nil {
		return h.err(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

// POST /store/v1/me/staff
func (h *StaffHandler) MeCreate(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	var in staffApp.NewPerson
	if !decodeStrict(c, &in) {
		return badBody(c)
	}
	v, err := h.svc.Create(c.Request().Context(), tid, lid, in)
	if err != nil {
		return h.err(c, err)
	}
	return c.JSON(http.StatusCreated, v)
}

// PATCH /store/v1/me/staff/:id
func (h *StaffHandler) MeUpdate(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	var ch staffApp.Change
	if !decodeStrict(c, &ch) {
		return badBody(c)
	}
	v, err := h.svc.Update(c.Request().Context(), tid, lid, id, ch)
	if err != nil {
		return h.err(c, err)
	}
	return c.JSON(http.StatusOK, v)
}

// POST /store/v1/me/staff/:id/pin {pin}
func (h *StaffHandler) MeResetPin(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	var in struct {
		Pin string `json:"pin"`
	}
	if !decodeStrict(c, &in) {
		return badBody(c)
	}
	if err := h.svc.ResetPin(c.Request().Context(), tid, lid, id, in.Pin); err != nil {
		return h.err(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// DELETE /store/v1/me/staff/:id
func (h *StaffHandler) MeDelete(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	if err := h.svc.Delete(c.Request().Context(), tid, lid, id); err != nil {
		return h.err(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// GET /store/v1/me/staff/journal?before=&limit= — the newest journal entries.
func (h *StaffHandler) MeJournal(c *echo.Context) error {
	_, lid, _ := meScope(c)
	var before *time.Time
	if b := c.QueryParam("before"); b != "" {
		t, err := time.Parse(time.RFC3339Nano, b)
		if err != nil {
			return c.JSON(http.StatusBadRequest, errCode("before must be RFC 3339", "bad_request"))
		}
		before = &t
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	out, err := h.svc.Journal(c.Request().Context(), lid, before, limit)
	if err != nil {
		return h.err(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

// GET /store/v1/me/terminal/settings
func (h *StaffHandler) MeSettings(c *echo.Context) error {
	_, lid, _ := meScope(c)
	st, _, err := h.svc.Settings(c.Request().Context(), lid)
	if err != nil {
		return h.err(c, err)
	}
	return c.JSON(http.StatusOK, st)
}

// PUT /store/v1/me/terminal/settings — the body over the saved settings: a
// field it leaves out keeps its saved value (older clients don't know the
// newer fields).
func (h *StaffHandler) MeSaveSettings(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	cur, _, err := h.svc.Settings(c.Request().Context(), lid)
	if err != nil {
		return h.err(c, err)
	}
	st := cur
	st.CardTipPercents = append([]int(nil), cur.CardTipPercents...)
	st.AreaCodes = append([]string(nil), cur.AreaCodes...)
	if !decodeStrict(c, &st) {
		return badBody(c)
	}
	out, err := h.svc.SaveSettings(c.Request().Context(), tid, lid, st)
	if err != nil {
		return h.err(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

// --- the Pi's pulls (agent key) ---------------------------------------------------

// GET /store/v1/agent/terminal-config — the settings + active staff with
// their PIN hashes. ETag "t<version>".
func (h *StaffHandler) TerminalConfig(c *echo.Context) error {
	scope, _, ok := agentScope(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, errResp("device required"))
	}
	cfg, err := h.svc.Config(c.Request().Context(), scope.LocationID)
	if err != nil {
		return h.err(c, err)
	}
	etag := fmt.Sprintf(`"t%d"`, cfg.Version)
	c.Response().Header().Set("ETag", etag)
	c.Response().Header().Set("Cache-Control", "no-store")
	if c.Request().Header.Get("If-None-Match") == etag {
		return c.NoContent(http.StatusNotModified)
	}
	return c.JSON(http.StatusOK, cfg)
}

// CounterPricing is what the Pi's engine needs besides the build's menu.
type CounterPricing struct {
	Items      map[string]pricing.ItemFacts `json:"items"`
	Settings   pricing.Settings             `json:"settings"`
	Deals      []pricing.Deal               `json:"deals"`
	Timezone   string                       `json:"timezone"`
	OpenPrices bool                         `json:"open_prices"`
}

// GET /store/v1/agent/counter-menu — the counter view's whole build (with
// its layout) + the counter's pricing. ETag "v<menu_version>".
func (h *StaffHandler) CounterMenu(c *echo.Context) error {
	scope, _, ok := agentScope(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, errResp("device required"))
	}
	ctx := c.Request().Context()
	ver, err := h.views.Version(ctx, scope.LocationID)
	if err != nil {
		return h.err(c, err)
	}
	etag := fmt.Sprintf(`"v%d"`, ver.Version)
	if c.Request().Header.Get("If-None-Match") == etag {
		c.Response().Header().Set("ETag", etag)
		return c.NoContent(http.StatusNotModified)
	}
	v, err := h.views.DefaultView(ctx, scope.TenantID, scope.LocationID, menuview.KindCounter)
	if err != nil {
		return h.err(c, err)
	}
	build, version, err := h.views.Build(ctx, v)
	if err != nil {
		return h.err(c, err)
	}
	pc, _, err := h.pricing.Context(ctx, scope.TenantID, scope.LocationID, string(menuview.KindCounter), &v.ID)
	if err != nil {
		return h.err(c, err)
	}
	routes := map[uuid.UUID]menuApp.ItemPrinter{}
	if h.menu != nil {
		if routes, err = h.menu.ItemPrinters(ctx, scope.LocationID); err != nil {
			return h.err(c, err)
		}
	}
	c.Response().Header().Set("ETag", fmt.Sprintf(`"v%d"`, version))
	return c.JSON(http.StatusOK, map[string]any{
		"menu_version": version,
		"view_id":      v.ID,
		"build":        build,
		"routes":       routes,
		"pricing": CounterPricing{Items: pc.Items, Settings: pc.Settings, Deals: pc.Deals, Timezone: pc.Timezone,
			OpenPrices: true},
	})
}
