package handler

import (
	"net/http"
	"strconv"
	"time"

	floorApp "quiccpos/main/internal/app/floor"
	orderSvc "quiccpos/main/internal/app/order"
	"quiccpos/main/internal/domain/order"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// FloorHandler serves the store manager's read-only view of the floor: recent
// orders with the status / kitchen number / driver the agent synced, the
// driver roster, and when the agent last synced.
type FloorHandler struct {
	orders *orderSvc.Service
	floor  *floorApp.Service
	logger zerolog.Logger
}

func NewFloorHandler(orders *orderSvc.Service, floor *floorApp.Service, logger zerolog.Logger) *FloorHandler {
	return &FloorHandler{orders: orders, floor: floor, logger: logger.With().Str("handler", "floor").Logger()}
}

// GET /store/v1/me/orders?limit=50 — newest-first snapshots for this location.
func (h *FloorHandler) MeListOrders(c *echo.Context) error {
	tid, lid, ok := meScope(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, errResp("unauthorized"))
	}
	limit := 50
	if v, err := strconv.Atoi(c.QueryParam("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	sc := order.Scope{TenantID: order.TenantID(tid), LocationID: order.LocationID(lid)}
	os, err := h.orders.GetOrdersPage(c.Request().Context(), sc, 0, limit)
	if err != nil {
		h.logger.Error().Err(err).Msg("list orders failed")
		return c.JSON(http.StatusInternalServerError, errResp("list failed"))
	}
	out := make([]order.Snapshot, 0, len(os))
	for _, o := range os {
		out = append(out, o.Snapshot())
	}
	return c.JSON(http.StatusOK, out)
}

// GET /store/v1/me/floor — {drivers, last_sync_at, shifts (last 7 days)} as
// the agent last synced.
func (h *FloorHandler) MeGetFloor(c *echo.Context) error {
	tid, lid, ok := meScope(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, errResp("unauthorized"))
	}
	ctx := c.Request().Context()
	sc := order.Scope{TenantID: order.TenantID(tid), LocationID: order.LocationID(lid)}
	drivers, err := h.floor.Drivers(ctx, sc)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("drivers failed"))
	}
	last, err := h.floor.LastSyncAt(ctx, sc)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("sync status failed"))
	}
	shifts, err := h.floor.Shifts(ctx, sc, time.Now().AddDate(0, 0, -7))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("shifts failed"))
	}
	return c.JSON(http.StatusOK, map[string]any{"drivers": drivers, "last_sync_at": last, "shifts": shifts})
}
