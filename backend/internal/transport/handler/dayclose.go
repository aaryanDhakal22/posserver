package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	daycloseApp "quiccpos/main/internal/app/dayclose"
	"quiccpos/main/internal/domain/dayclose"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// DayCloseHandler serves the store's drawer sessions and Z reports as the Pi
// reported them (terminal Stage 2, contract/main.md §2.5zb). Read only: the
// Pi decides every number; no UI yet.
type DayCloseHandler struct {
	svc    *daycloseApp.Service
	logger zerolog.Logger
}

func NewDayCloseHandler(svc *daycloseApp.Service, lg zerolog.Logger) *DayCloseHandler {
	return &DayCloseHandler{svc: svc, logger: lg.With().Str("module", "dayclose-handler").Logger()}
}

// dateParam reads ?date=YYYY-MM-DD (nil when absent; ok false after a 400).
func dateParam(c *echo.Context) (*time.Time, bool, error) {
	raw := c.QueryParam("date")
	if raw == "" {
		return nil, true, nil
	}
	d, err := dayclose.ParseDate(raw)
	if err != nil {
		return nil, false, c.JSON(http.StatusBadRequest, errCode("date must be YYYY-MM-DD", "bad_date"))
	}
	return &d, true, nil
}

func (h *DayCloseHandler) fail(c *echo.Context, err error) error {
	if errors.Is(err, daycloseApp.ErrNotFound) {
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	}
	h.logger.Error().Ctx(c.Request().Context()).Err(err).Msg("day close")
	return c.JSON(http.StatusInternalServerError, errCode("day close failed", "internal"))
}

// GET /store/v1/me/reports/z?date=YYYY-MM-DD
func (h *DayCloseHandler) MeZList(c *echo.Context) error {
	_, lid, _ := meScope(c)
	date, ok, err := dateParam(c)
	if !ok {
		return err
	}
	list, err := h.svc.ZReports(c.Request().Context(), lid, date)
	if err != nil {
		return h.fail(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"reports": list})
}

// GET /store/v1/me/reports/z/:id
func (h *DayCloseHandler) MeZ(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	}
	z, err := h.svc.ZReport(c.Request().Context(), lid, id)
	if err != nil {
		return h.fail(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"report": json.RawMessage(z.Report), "received_at": z.ReceivedAt})
}

// GET /store/v1/me/drawers?date=YYYY-MM-DD
func (h *DayCloseHandler) MeDrawers(c *echo.Context) error {
	_, lid, _ := meScope(c)
	date, ok, err := dateParam(c)
	if !ok {
		return err
	}
	list, err := h.svc.Drawers(c.Request().Context(), lid, date)
	if err != nil {
		return h.fail(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"drawers": list})
}

// GET /store/v1/me/drawers/:id
func (h *DayCloseHandler) MeDrawer(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	}
	d, moves, err := h.svc.Drawer(c.Request().Context(), lid, id)
	if err != nil {
		return h.fail(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"drawer": d, "movements": moves})
}
