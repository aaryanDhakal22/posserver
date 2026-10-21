package handler

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	menuApp "quiccpos/main/internal/app/menu"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// MenuHandler serves /store/v1/me/menu/* — session-scoped (scope from the
// cookie via meScope). Every mutation is ownership-checked in the service.
type MenuHandler struct {
	svc    *menuApp.Service
	logger zerolog.Logger
}

func NewMenuHandler(svc *menuApp.Service, logger zerolog.Logger) *MenuHandler {
	return &MenuHandler{svc: svc, logger: logger.With().Str("module", "menu-handler").Logger()}
}

// mErr maps a service error to an HTTP status.
// GET /store/v1/me/menu/unmatched?days=14 — recent vendor item names that
// don't route to a kitchen printer (unmatched / unassigned), most-ordered first.
func (h *MenuHandler) MeUnmatched(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	days := 14
	if v, err := strconv.Atoi(c.QueryParam("days")); err == nil && v > 0 && v <= 365 {
		days = v
	}
	out, err := h.svc.UnmatchedNames(c.Request().Context(), tid, lid, time.Now().AddDate(0, 0, -days))
	if err != nil {
		return h.mErr(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

func (h *MenuHandler) mErr(c *echo.Context, err error) error {
	if menuApp.IsNotFound(err) {
		return c.JSON(http.StatusNotFound, errResp("not found"))
	}
	return c.JSON(http.StatusBadRequest, errResp(err.Error()))
}

func idParam(c *echo.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	return id, err == nil
}

func optUUID(s *string) (*uuid.UUID, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	u, err := uuid.Parse(*s)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func parseIDs(raw []string) ([]uuid.UUID, error) {
	out := make([]uuid.UUID, 0, len(raw))
	for _, s := range raw {
		u, err := uuid.Parse(s)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// --- Menus -----------------------------------------------------------------

func (h *MenuHandler) MeListMenus(c *echo.Context) error {
	_, lid, _ := meScope(c)
	rows, err := h.svc.ListMenus(c.Request().Context(), lid)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("list failed"))
	}
	return c.JSON(http.StatusOK, rows)
}

func (h *MenuHandler) MeCreateMenu(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	var req struct{ Name, Description string }
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	m, err := h.svc.CreateMenu(c.Request().Context(), tid, lid, req.Name, req.Description)
	if err != nil {
		return h.mErr(c, err)
	}
	return c.JSON(http.StatusCreated, m)
}

func (h *MenuHandler) MeUpdateMenu(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Available   bool   `json:"available"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	m, err := h.svc.UpdateMenu(c.Request().Context(), tid, lid, id, req.Name, req.Description, req.Available)
	if err != nil {
		return h.mErr(c, err)
	}
	return c.JSON(http.StatusOK, m)
}

func (h *MenuHandler) MeDeleteMenu(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	if err := h.svc.DeleteMenu(c.Request().Context(), tid, lid, id); err != nil {
		return h.mErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *MenuHandler) MeReorderMenus(c *echo.Context) error {
	tid, _, _ := meScope(c)
	return h.reorder(c, func(ids []uuid.UUID) error { return h.svc.ReorderMenus(c.Request().Context(), tid, ids) })
}

// --- Categories ------------------------------------------------------------

func (h *MenuHandler) MeListCategories(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	menuID, ok := idParam(c, "id")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	rows, err := h.svc.ListCategories(c.Request().Context(), tid, lid, menuID)
	if err != nil {
		return h.mErr(c, err)
	}
	return c.JSON(http.StatusOK, rows)
}

func (h *MenuHandler) MeCreateCategory(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	menuID, ok := idParam(c, "id")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	var req struct {
		Name           string  `json:"name"`
		Description    string  `json:"description"`
		DefaultPrinter *string `json:"default_printer_id"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	pr, err := optUUID(req.DefaultPrinter)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("default_printer_id must be a uuid"))
	}
	cat, err := h.svc.CreateCategory(c.Request().Context(), tid, lid, menuID, req.Name, req.Description, pr)
	if err != nil {
		return h.mErr(c, err)
	}
	return c.JSON(http.StatusCreated, cat)
}

func (h *MenuHandler) MeUpdateCategory(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	var req struct {
		Name           string  `json:"name"`
		Description    string  `json:"description"`
		Available      bool    `json:"available"`
		DefaultPrinter *string `json:"default_printer_id"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	pr, err := optUUID(req.DefaultPrinter)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("default_printer_id must be a uuid"))
	}
	cat, err := h.svc.UpdateCategory(c.Request().Context(), tid, lid, id, req.Name, req.Description, req.Available, pr)
	if err != nil {
		return h.mErr(c, err)
	}
	return c.JSON(http.StatusOK, cat)
}

func (h *MenuHandler) MeDeleteCategory(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	if err := h.svc.DeleteCategory(c.Request().Context(), tid, lid, id); err != nil {
		return h.mErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *MenuHandler) MeReorderCategories(c *echo.Context) error {
	tid, _, _ := meScope(c)
	return h.reorder(c, func(ids []uuid.UUID) error { return h.svc.ReorderCategories(c.Request().Context(), tid, ids) })
}

// --- Items -----------------------------------------------------------------

func (h *MenuHandler) MeListItems(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	catID, ok := idParam(c, "id")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	rows, err := h.svc.ListItems(c.Request().Context(), tid, lid, catID)
	if err != nil {
		return h.mErr(c, err)
	}
	return c.JSON(http.StatusOK, rows)
}

func (h *MenuHandler) MeCreateItem(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	catID, ok := idParam(c, "id")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	var req struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		PriceCents  int64    `json:"price_cents"`
		TaxRateBps  int      `json:"tax_rate_bps"`
		Tags        []string `json:"tags"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	it, err := h.svc.CreateItem(c.Request().Context(), tid, lid, catID, req.Name, req.Description, req.PriceCents, req.TaxRateBps, req.Tags)
	if err != nil {
		return h.mErr(c, err)
	}
	return c.JSON(http.StatusCreated, it)
}

func (h *MenuHandler) MeUpdateItem(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	var req struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		PriceCents  int64          `json:"price_cents"`
		TaxRateBps  int            `json:"tax_rate_bps"`
		InStock     bool           `json:"in_stock"`
		OutUntil    *time.Time     `json:"out_until"`
		Tags        []string       `json:"tags"`
		Attributes  map[string]any `json:"attributes"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	it, err := h.svc.UpdateItem(c.Request().Context(), tid, lid, id, menuApp.UpdateItemInput{
		Name: req.Name, Description: req.Description, PriceCents: req.PriceCents, TaxRateBps: req.TaxRateBps,
		InStock: req.InStock, OutUntil: req.OutUntil, Tags: req.Tags, Attributes: req.Attributes,
	})
	if err != nil {
		return h.mErr(c, err)
	}
	return c.JSON(http.StatusOK, it)
}

func (h *MenuHandler) MeAssignItemPrinter(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	var req struct {
		PrinterID *string `json:"printer_id"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	pr, err := optUUID(req.PrinterID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("printer_id must be a uuid"))
	}
	if err := h.svc.AssignItemPrinter(c.Request().Context(), tid, lid, id, pr); err != nil {
		return h.mErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *MenuHandler) MeDeleteItem(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	if err := h.svc.DeleteItem(c.Request().Context(), tid, lid, id); err != nil {
		return h.mErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *MenuHandler) MeReorderItems(c *echo.Context) error {
	tid, _, _ := meScope(c)
	return h.reorder(c, func(ids []uuid.UUID) error { return h.svc.ReorderItems(c.Request().Context(), tid, ids) })
}

// --- Modifier groups + options ---------------------------------------------

func (h *MenuHandler) MeListGroups(c *echo.Context) error {
	_, lid, _ := meScope(c)
	rows, err := h.svc.ListGroups(c.Request().Context(), lid)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("list failed"))
	}
	return c.JSON(http.StatusOK, rows)
}

type groupBody struct {
	Name          string `json:"name"`
	Required      bool   `json:"required"`
	MinSelect     int    `json:"min_select"`
	MaxSelect     int    `json:"max_select"`
	AllowMultiple bool   `json:"allow_multiple"`
}

func (b groupBody) rules() menuApp.GroupRules {
	return menuApp.GroupRules{Required: b.Required, MinSelect: b.MinSelect, MaxSelect: b.MaxSelect, AllowMultiple: b.AllowMultiple}
}

func (h *MenuHandler) MeCreateGroup(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	var req groupBody
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	g, err := h.svc.CreateGroup(c.Request().Context(), tid, lid, req.Name, req.rules())
	if err != nil {
		return h.mErr(c, err)
	}
	return c.JSON(http.StatusCreated, g)
}

func (h *MenuHandler) MeUpdateGroup(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	var req groupBody
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	g, err := h.svc.UpdateGroup(c.Request().Context(), tid, lid, id, req.Name, req.rules())
	if err != nil {
		return h.mErr(c, err)
	}
	return c.JSON(http.StatusOK, g)
}

func (h *MenuHandler) MeDeleteGroup(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	if err := h.svc.DeleteGroup(c.Request().Context(), tid, lid, id); err != nil {
		return h.mErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *MenuHandler) MeListOptions(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	gid, ok := idParam(c, "id")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	rows, err := h.svc.ListOptions(c.Request().Context(), tid, lid, gid)
	if err != nil {
		return h.mErr(c, err)
	}
	return c.JSON(http.StatusOK, rows)
}

func (h *MenuHandler) MeCreateOption(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	gid, ok := idParam(c, "id")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	var req struct {
		Name            string `json:"name"`
		PriceDeltaCents int64  `json:"price_delta_cents"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	o, err := h.svc.CreateOption(c.Request().Context(), tid, lid, gid, req.Name, req.PriceDeltaCents)
	if err != nil {
		return h.mErr(c, err)
	}
	return c.JSON(http.StatusCreated, o)
}

func (h *MenuHandler) MeUpdateOption(c *echo.Context) error {
	tid, _, _ := meScope(c)
	optID, ok := idParam(c, "optId")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("optId must be a uuid"))
	}
	var req struct {
		Name            string `json:"name"`
		PriceDeltaCents int64  `json:"price_delta_cents"`
		InStock         bool   `json:"in_stock"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	o, err := h.svc.UpdateOption(c.Request().Context(), tid, uuid.Nil, optID, req.Name, req.PriceDeltaCents, req.InStock)
	if err != nil {
		return h.mErr(c, err)
	}
	return c.JSON(http.StatusOK, o)
}

func (h *MenuHandler) MeDeleteOption(c *echo.Context) error {
	tid, _, _ := meScope(c)
	optID, ok := idParam(c, "optId")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("optId must be a uuid"))
	}
	if err := h.svc.DeleteOption(c.Request().Context(), tid, optID); err != nil {
		return h.mErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *MenuHandler) MeReorderOptions(c *echo.Context) error {
	tid, _, _ := meScope(c)
	return h.reorder(c, func(ids []uuid.UUID) error { return h.svc.ReorderOptions(c.Request().Context(), tid, ids) })
}

// --- Item ↔ group ----------------------------------------------------------

func (h *MenuHandler) MeListItemGroups(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	itemID, ok := idParam(c, "id")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	rows, err := h.svc.ListGroupsForItem(c.Request().Context(), tid, lid, itemID)
	if err != nil {
		return h.mErr(c, err)
	}
	return c.JSON(http.StatusOK, rows)
}

func (h *MenuHandler) MeAttachGroup(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	itemID, ok := idParam(c, "id")
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	var req struct {
		GroupID string `json:"group_id"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	gid, err := uuid.Parse(req.GroupID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("group_id must be a uuid"))
	}
	if err := h.svc.AttachGroup(c.Request().Context(), tid, lid, itemID, gid); err != nil {
		return h.mErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *MenuHandler) MeDetachGroup(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	itemID, ok := idParam(c, "id")
	gid, ok2 := idParam(c, "groupId")
	if !ok || !ok2 {
		return c.JSON(http.StatusBadRequest, errResp("id and groupId must be uuids"))
	}
	if err := h.svc.DetachGroup(c.Request().Context(), tid, lid, itemID, gid); err != nil {
		return h.mErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// reorder is the shared handler for the /reorder endpoints: body {ids:[...]}.
func (h *MenuHandler) reorder(c *echo.Context, apply func([]uuid.UUID) error) error {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	ids, err := parseIDs(req.IDs)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("ids must be uuids"))
	}
	if err := apply(ids); err != nil {
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	return c.NoContent(http.StatusNoContent)
}

// --- CSV import / export ------------------------------------------------------

// maxMenuCSV bounds an import upload.
const maxMenuCSV = 2 << 20

// MeExportCSV — GET /store/v1/me/menu/export.csv: the location's menus as an
// import file (menu,category,item,price,printer,description).
func (h *MenuHandler) MeExportCSV(c *echo.Context) error {
	_, lid, _ := meScope(c)
	var buf bytes.Buffer
	if err := h.svc.ExportCSV(c.Request().Context(), lid, &buf); err != nil {
		h.logger.Error().Ctx(c.Request().Context()).Err(err).Msg("menu export failed")
		return c.JSON(http.StatusInternalServerError, errResp("export failed"))
	}
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="menu.csv"`)
	return c.Blob(http.StatusOK, "text/csv; charset=utf-8", buf.Bytes())
}

// MeImportCSV — POST /store/v1/me/menu/import?dry_run=1&replace=1, body = the
// CSV. Returns what changed (or would change). Bad rows / unknown printers → 400.
func (h *MenuHandler) MeImportCSV(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, maxMenuCSV+1))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("could not read the file"))
	}
	if len(body) > maxMenuCSV {
		return c.JSON(http.StatusRequestEntityTooLarge, errResp("file too large (max 2 MB)"))
	}
	rows, err := menuApp.ParseMenuCSV(bytes.NewReader(body))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	dry, replace := truthy(c.QueryParam("dry_run")), truthy(c.QueryParam("replace"))
	res, err := h.svc.ImportCSV(c.Request().Context(), tid, lid, rows, dry, replace)
	if err != nil {
		if menuApp.IsCSVError(err) {
			return c.JSON(http.StatusBadRequest, errResp(err.Error()))
		}
		h.logger.Error().Ctx(c.Request().Context()).Err(err).Bool("dry_run", dry).Msg("menu import failed")
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "import failed part-way: " + err.Error() + " — fix and re-run (imports are re-runnable)", "result": res})
	}
	return c.JSON(http.StatusOK, res)
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
