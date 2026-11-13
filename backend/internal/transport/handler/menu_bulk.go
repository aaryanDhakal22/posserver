package handler

import (
	"errors"
	"net/http"

	menubulk "quiccpos/main/internal/app/menubulk"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// MenuBulkHandler is Store › Menu's bulk edit (POST /store/v1/me/menu/items/bulk).
type MenuBulkHandler struct {
	svc    *menubulk.Service
	logger zerolog.Logger
}

func NewMenuBulkHandler(svc *menubulk.Service, lg zerolog.Logger) *MenuBulkHandler {
	return &MenuBulkHandler{svc: svc, logger: lg.With().Str("module", "menu-bulk-handler").Logger()}
}

// MeApply changes several items at once, all or nothing: body
// {changes: [{item_id, category_id?, printer_id? ("" = the category's),
// no_kitchen_ticket?, in_stock?, price_cents?, size_prices?, tax_rate_bps?}]}
// → 200 {updated}.
func (h *MenuBulkHandler) MeApply(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	var req struct {
		Changes []menubulk.Change `json:"changes"`
	}
	if err := echo.BindBody(c, &req); err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid body", "bad_body"))
	}
	n, err := h.svc.Apply(c.Request().Context(), tid, lid, req.Changes)
	switch {
	case err == nil:
		return c.JSON(http.StatusOK, map[string]int{"updated": n})
	case errors.Is(err, menubulk.ErrBadBody):
		return c.JSON(http.StatusBadRequest, errCode(err.Error(), "bad_body"))
	case errors.Is(err, menubulk.ErrBadPrice):
		return c.JSON(http.StatusBadRequest, errCode(err.Error(), "bad_price"))
	case errors.Is(err, menubulk.ErrUnknownSize):
		return c.JSON(http.StatusBadRequest, errCode(err.Error(), "unknown_size"))
	case errors.Is(err, menubulk.ErrNotFound):
		return c.JSON(http.StatusNotFound, errCode(err.Error(), "not_found"))
	case errors.Is(err, menubulk.ErrQuickAdd):
		return c.JSON(http.StatusConflict, errCode(err.Error(), "quick_add_kept"))
	default:
		h.logger.Error().Err(err).Msg("menu bulk edit failed")
		return c.JSON(http.StatusInternalServerError, errResp("couldn't save the changes"))
	}
}
