package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	menuviewApp "quiccpos/main/internal/app/menuview"
	channelDom "quiccpos/main/internal/domain/channel"
	menuDom "quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/menuview"
	appMiddleware "quiccpos/main/internal/transport/middleware"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// MenuViewHandler serves the menu bank + views (main 3.0): the store
// manager's /store/v1/me/menu/{bank,sizes,views,entries,vendor} (session) and
// the channels' /channel/v1/menu (channel key).
type MenuViewHandler struct {
	svc    *menuviewApp.Service
	logger zerolog.Logger
}

func NewMenuViewHandler(svc *menuviewApp.Service, logger zerolog.Logger) *MenuViewHandler {
	return &MenuViewHandler{svc: svc, logger: logger.With().Str("module", "menuview-handler").Logger()}
}

func (h *MenuViewHandler) vErr(c *echo.Context, err error) error {
	switch {
	case errors.Is(err, menuviewApp.ErrNotFound):
		return c.JSON(http.StatusNotFound, errCode(err.Error(), "not_found"))
	case errors.Is(err, menuviewApp.ErrSizeTaken):
		return c.JSON(http.StatusConflict, errCode(err.Error(), "size_taken"))
	case errors.Is(err, menuviewApp.ErrBadSize), errors.Is(err, menuviewApp.ErrBadPrice), errors.Is(err, menuviewApp.ErrUnknownSize),
		errors.Is(err, menuviewApp.ErrBadKind), errors.Is(err, menuviewApp.ErrBadName), errors.Is(err, menuviewApp.ErrBadRules),
		errors.Is(err, menuviewApp.ErrBadEntry):
		return c.JSON(http.StatusBadRequest, errCode(err.Error(), "bad_request"))
	}
	h.logger.Error().Err(err).Msg("menu views")
	return c.JSON(http.StatusInternalServerError, errCode("menu views failed", "internal"))
}

func badBody(c *echo.Context) error {
	return c.JSON(http.StatusBadRequest, errCode("invalid body", "bad_request"))
}

func badID(c *echo.Context) error {
	return c.JSON(http.StatusBadRequest, errCode("id must be a uuid", "bad_request"))
}

// --- the bank -------------------------------------------------------------------

// GET /store/v1/me/menu/bank — the whole bank (menus → categories → items with
// sizes + groups, the groups with their options, the store's sizes).
func (h *MenuViewHandler) MeBank(c *echo.Context) error {
	_, lid, _ := meScope(c)
	b, err := h.svc.Bank(c.Request().Context(), lid)
	if err != nil {
		return h.vErr(c, err)
	}
	return c.JSON(http.StatusOK, b)
}

func (h *MenuViewHandler) MeListSizes(c *echo.Context) error {
	_, lid, _ := meScope(c)
	out, err := h.svc.Sizes(c.Request().Context(), lid)
	if err != nil {
		return h.vErr(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

type sizeReq struct {
	Name  string `json:"name"`
	Short string `json:"short"`
}

func (h *MenuViewHandler) MeCreateSize(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	var req sizeReq
	if err := echo.BindBody(c, &req); err != nil {
		return badBody(c)
	}
	sz, err := h.svc.CreateSize(c.Request().Context(), tid, lid, req.Name, req.Short)
	if err != nil {
		return h.vErr(c, err)
	}
	return c.JSON(http.StatusCreated, sz)
}

func (h *MenuViewHandler) MeUpdateSize(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	var req sizeReq
	if err := echo.BindBody(c, &req); err != nil {
		return badBody(c)
	}
	sz, err := h.svc.UpdateSize(c.Request().Context(), tid, lid, id, req.Name, req.Short)
	if err != nil {
		return h.vErr(c, err)
	}
	return c.JSON(http.StatusOK, sz)
}

func (h *MenuViewHandler) MeReorderSizes(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	var req struct {
		IDs []uuid.UUID `json:"ids"`
	}
	if err := echo.BindBody(c, &req); err != nil {
		return badBody(c)
	}
	if err := h.svc.ReorderSizes(c.Request().Context(), tid, lid, req.IDs); err != nil {
		return h.vErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *MenuViewHandler) MeDeleteSize(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	if err := h.svc.DeleteSize(c.Request().Context(), tid, lid, id); err != nil {
		return h.vErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// PUT /store/v1/me/menu/items/:id/bank {kitchen_name, sku, sizes:[{size_id, price_cents, kitchen_name}]}
func (h *MenuViewHandler) MeSetItemBank(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	var req menuDom.ItemExtra
	if err := echo.BindBody(c, &req); err != nil {
		return badBody(c)
	}
	for i := range req.Sizes {
		req.Sizes[i].Position = i
	}
	if err := h.svc.SetItemBank(c.Request().Context(), tid, lid, id, req); err != nil {
		return h.vErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// PUT /store/v1/me/menu/modifier-groups/:id/bank {portions, options:[{option_id, is_default, kitchen_name, prices}]}
func (h *MenuViewHandler) MeSetGroupBank(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	var req menuDom.GroupExtra
	if err := echo.BindBody(c, &req); err != nil {
		return badBody(c)
	}
	if err := h.svc.SetGroupBank(c.Request().Context(), tid, lid, id, req); err != nil {
		return h.vErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// --- views ----------------------------------------------------------------------

// GET /store/v1/me/menu/views — every view (the default one of each kind is
// created on first use).
func (h *MenuViewHandler) MeListViews(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	out, err := h.svc.Views(c.Request().Context(), tid, lid)
	if err != nil {
		return h.vErr(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

func (h *MenuViewHandler) MeCreateView(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	var req struct {
		Kind  menuview.Kind  `json:"kind"`
		Name  string         `json:"name"`
		Rules menuview.Rules `json:"rules"`
	}
	if err := echo.BindBody(c, &req); err != nil {
		return badBody(c)
	}
	v, err := h.svc.CreateView(c.Request().Context(), tid, lid, req.Kind, req.Name, req.Rules)
	if err != nil {
		return h.vErr(c, err)
	}
	return c.JSON(http.StatusCreated, v)
}

func (h *MenuViewHandler) MeGetView(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	v, err := h.svc.View(c.Request().Context(), lid, id)
	if err != nil {
		return h.vErr(c, err)
	}
	return c.JSON(http.StatusOK, v)
}

func (h *MenuViewHandler) MeUpdateView(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	var req struct {
		Name  *string         `json:"name"`
		Rules *menuview.Rules `json:"rules"`
	}
	if err := echo.BindBody(c, &req); err != nil {
		return badBody(c)
	}
	v, err := h.svc.UpdateView(c.Request().Context(), lid, id, req.Name, req.Rules)
	if err != nil {
		return h.vErr(c, err)
	}
	return c.JSON(http.StatusOK, v)
}

func (h *MenuViewHandler) MeDeleteView(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	if err := h.svc.DeleteView(c.Request().Context(), lid, id); err != nil {
		return h.vErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// GET /store/v1/me/menu/views/:id/build — the view as its channels see it,
// plus maps (vendor views) and the needs-attention list.
func (h *MenuViewHandler) MeBuildView(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	ctx := c.Request().Context()
	v, err := h.svc.View(ctx, lid, id)
	if err != nil {
		return h.vErr(c, err)
	}
	raw, _, err := h.svc.Build(ctx, v)
	if err != nil {
		return h.vErr(c, err)
	}
	return c.Blob(http.StatusOK, "application/json", raw)
}

// MeProblems is Store › Menu › Find problems: items the counter can't ring,
// items that print nowhere, prices that look missing and broken modifier
// groups (domain/menucheck).
func (h *MenuViewHandler) MeProblems(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	out, err := h.svc.Problems(c.Request().Context(), tid, lid)
	if err != nil {
		return h.vErr(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

// --- entries --------------------------------------------------------------------

func (h *MenuViewHandler) MeListEntries(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	out, err := h.svc.Entries(c.Request().Context(), lid, id)
	if err != nil {
		return h.vErr(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

func (h *MenuViewHandler) MeCreateEntry(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	var e menuview.Entry
	if err := echo.BindBody(c, &e); err != nil {
		return badBody(c)
	}
	out, err := h.svc.CreateEntry(c.Request().Context(), lid, id, e)
	if err != nil {
		return h.vErr(c, err)
	}
	return c.JSON(http.StatusCreated, out)
}

// PUT /store/v1/me/menu/entries/:id — replaces the entry (its kind and view stay).
func (h *MenuViewHandler) MeUpdateEntry(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	var e menuview.Entry
	if err := echo.BindBody(c, &e); err != nil {
		return badBody(c)
	}
	out, err := h.svc.UpdateEntry(c.Request().Context(), lid, id, e)
	if err != nil {
		return h.vErr(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

func (h *MenuViewHandler) MeDeleteEntry(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	if err := h.svc.DeleteEntry(c.Request().Context(), lid, id); err != nil {
		return h.vErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// --- the vendor view (Brygid) ---------------------------------------------------

// POST /store/v1/me/menu/vendor/maps {vendor_name, item_id} — map a vendor
// item name to a bank item (the needs-attention picker).
func (h *MenuViewHandler) MeVendorMap(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	var req struct {
		VendorName string     `json:"vendor_name"`
		VendorSize string     `json:"vendor_size"`
		ItemID     uuid.UUID  `json:"item_id"`
		SizeID     *uuid.UUID `json:"size_id"`
	}
	if err := echo.BindBody(c, &req); err != nil {
		return badBody(c)
	}
	ctx := c.Request().Context()
	v, err := h.svc.DefaultView(ctx, tid, lid, menuview.KindVendor)
	if err != nil {
		return h.vErr(c, err)
	}
	e, err := h.svc.CreateEntry(ctx, lid, v.ID, menuview.Entry{Kind: menuview.EntryMap,
		Anchor: menuview.Anchor{ItemIDs: []uuid.UUID{req.ItemID}, SizeID: req.SizeID},
		Extra:  menuview.Extra{VendorName: req.VendorName, VendorSize: req.VendorSize}})
	if err != nil {
		return h.vErr(c, err)
	}
	return c.JSON(http.StatusCreated, e)
}

// --- /channel/v1/menu -----------------------------------------------------------

// channelView is the view a channel reads.
func (h *MenuViewHandler) channelView(c *echo.Context, ch *channelDom.Channel) (*menuview.View, error) {
	return h.svc.ViewFor(c.Request().Context(), ch.TenantID, ch.LocationID, menuview.Kind(ch.Kind), ch.ViewID)
}

func menuChannel(c *echo.Context) (*channelDom.Channel, error) {
	ch, ok := appMiddleware.ChannelFromContext(c.Request().Context())
	if !ok {
		return nil, c.JSON(http.StatusUnauthorized, errCode("missing channel key", "unauthorized"))
	}
	if !ch.Permissions.Menu {
		return nil, c.JSON(http.StatusForbidden, errCode("this channel may not read the menu", "forbidden"))
	}
	return &ch, nil
}

// GET /channel/v1/menu — the channel's menu. ETag = the menu version; a
// matching If-None-Match is a 304.
func (h *MenuViewHandler) ChannelMenu(c *echo.Context) error {
	ch, err := menuChannel(c)
	if ch == nil {
		return err
	}
	v, err := h.channelView(c, ch)
	if err != nil {
		return h.chErr(c, err)
	}
	raw, ver, err := h.svc.ChannelBuild(c.Request().Context(), v)
	if err != nil {
		return h.chErr(c, err)
	}
	etag := fmt.Sprintf(`"v%d"`, ver)
	c.Response().Header().Set("ETag", etag)
	c.Response().Header().Set("Cache-Control", "no-cache")
	if c.Request().Header.Get("If-None-Match") == etag {
		return c.NoContent(http.StatusNotModified)
	}
	return c.Blob(http.StatusOK, "application/json", raw)
}

// GET /channel/v1/menu/version?since=N — the menu's version, and whether it
// changed after version N (a cheap poll before refetching the menu).
func (h *MenuViewHandler) ChannelMenuVersion(c *echo.Context) error {
	ch, err := menuChannel(c)
	if ch == nil {
		return err
	}
	ver, err := h.svc.Version(c.Request().Context(), ch.LocationID)
	if err != nil {
		return h.chErr(c, err)
	}
	out := map[string]any{"version": ver.Version, "changed_at": ver.ChangedAt}
	if s := c.QueryParam("since"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 0 {
			return c.JSON(http.StatusBadRequest, errCode("since must be a version number", "bad_request"))
		}
		out["changed"] = ver.Version > n
	}
	return c.JSON(http.StatusOK, out)
}

func (h *MenuViewHandler) chErr(c *echo.Context, err error) error {
	h.logger.Error().Err(err).Msg("channel menu")
	return c.JSON(http.StatusInternalServerError, errCode("menu unavailable", "internal"))
}
