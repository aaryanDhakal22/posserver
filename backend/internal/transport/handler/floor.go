package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	floorApp "quiccpos/main/internal/app/floor"
	orderSvc "quiccpos/main/internal/app/order"
	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/repositories"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// FloorHandler serves the store manager's read-only view of the floor: recent
// orders with the status / kitchen number / driver the agent synced, the
// driver roster, and when the agent last synced.
type FloorHandler struct {
	orders *orderSvc.Service
	floor  *floorApp.Service
	detail *repositories.OrderDetailRepository
	logger zerolog.Logger
}

func NewFloorHandler(orders *orderSvc.Service, floor *floorApp.Service, detail *repositories.OrderDetailRepository, logger zerolog.Logger) *FloorHandler {
	return &FloorHandler{orders: orders, floor: floor, detail: detail, logger: logger.With().Str("handler", "floor").Logger()}
}

// GET /store/v1/me/orders?limit=50 — newest-first snapshots for this location.
// sort=placed (Store › Orders): newest *placed* first, with the optional
// date=YYYY-MM-DD (a store-local day) and q (order / kitchen # / customer /
// phone / driver). Without them: newest received first, as before.
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
	day, q := c.QueryParam("date"), strings.TrimSpace(c.QueryParam("q"))
	if h.detail != nil && (c.QueryParam("sort") == "placed" || day != "" || q != "") {
		if day != "" {
			if _, err := time.Parse("2006-01-02", day); err != nil {
				return c.JSON(http.StatusBadRequest, errCode("date must be YYYY-MM-DD", "bad_date"))
			}
		}
		if len(q) > 100 {
			q = q[:100]
		}
		ids, err := h.detail.ListIDs(c.Request().Context(), tid, lid, day, q, limit)
		if err != nil {
			h.logger.Error().Err(err).Msg("list orders failed")
			return c.JSON(http.StatusInternalServerError, errResp("list failed"))
		}
		out := make([]order.Snapshot, 0, len(ids))
		for _, id := range ids {
			o, err := h.orders.GetByID(c.Request().Context(), sc, order.ID(id))
			if err != nil {
				continue // deleted meanwhile
			}
			out = append(out, o.Snapshot())
		}
		return c.JSON(http.StatusOK, out)
	}
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

// GET /store/v1/me/orders/:id — one order with everything Store › Orders'
// detail panel shows: {order: Snapshot, …OrderDetailExtras}. 404 not_found
// for an unknown id or another store's order.
func (h *FloorHandler) MeGetOrder(c *echo.Context) error {
	tid, lid, ok := meScope(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, errResp("unauthorized"))
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusNotFound, errCode("order not found", "not_found"))
	}
	ctx := c.Request().Context()
	sc := order.Scope{TenantID: order.TenantID(tid), LocationID: order.LocationID(lid)}
	extras, err := h.detail.Extras(ctx, tid, lid, id)
	if errors.Is(err, repositories.ErrOrderNotInStore) {
		return c.JSON(http.StatusNotFound, errCode("order not found", "not_found"))
	}
	if err != nil {
		h.logger.Error().Err(err).Msg("order detail failed")
		return c.JSON(http.StatusInternalServerError, errCode("order detail failed", "internal"))
	}
	o, err := h.orders.GetByID(ctx, sc, order.ID(id))
	if err != nil {
		return c.JSON(http.StatusNotFound, errCode("order not found", "not_found"))
	}
	return c.JSON(http.StatusOK, struct {
		Order order.Snapshot `json:"order"`
		*repositories.OrderDetailExtras
	}{o.Snapshot(), extras})
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

type deliveryRunJSON struct {
	ID           string          `json:"id"`
	BatchID      *int64          `json:"batch_id"`
	DriverID     string          `json:"driver_id"`
	DriverName   string          `json:"driver_name"`
	PlanID       string          `json:"plan_id"`
	SuggestedIDs []string        `json:"suggested_ids"`
	SentIDs      []string        `json:"sent_ids"`
	Stops        json.RawMessage `json:"stops"`
	At           time.Time       `json:"at"`
}

func (h *FloorHandler) deliveryRuns(c *echo.Context, sc order.Scope) error {
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	runs, err := h.floor.DeliveryRuns(c.Request().Context(), sc, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("delivery runs failed"))
	}
	out := make([]deliveryRunJSON, 0, len(runs))
	for _, r := range runs {
		out = append(out, deliveryRunJSON{ID: r.ID.String(), BatchID: r.BatchID, DriverID: r.DriverID, DriverName: r.DriverName,
			PlanID: r.PlanID, SuggestedIDs: nonNilIDs(r.SuggestedIDs), SentIDs: nonNilIDs(r.SentIDs), Stops: r.Stops, At: r.At})
	}
	return c.JSON(http.StatusOK, out)
}

func nonNilIDs(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// GET /store/v1/me/delivery-runs?limit=50 — the driver runs the agent sent
// (AUTO suggestion vs sent), newest first.
func (h *FloorHandler) MeDeliveryRuns(c *echo.Context) error {
	tid, lid, ok := meScope(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, errResp("unauthorized"))
	}
	return h.deliveryRuns(c, order.Scope{TenantID: order.TenantID(tid), LocationID: order.LocationID(lid)})
}

// GET /admin/v1/delivery-runs?tenant_id&location_id&limit=50
func (h *FloorHandler) AdminDeliveryRuns(c *echo.Context) error {
	tid, err1 := uuid.Parse(c.QueryParam("tenant_id"))
	lid, err2 := uuid.Parse(c.QueryParam("location_id"))
	if err1 != nil || err2 != nil {
		return c.JSON(http.StatusBadRequest, errResp("tenant_id and location_id must be uuids"))
	}
	return h.deliveryRuns(c, order.Scope{TenantID: order.TenantID(tid), LocationID: order.LocationID(lid)})
}
