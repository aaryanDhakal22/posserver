package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"quiccpos/main/internal/app/orderport"
	"quiccpos/main/internal/domain/notify"
	appMiddleware "quiccpos/main/internal/transport/middleware"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// OrderPortHandler serves /channel/v1/orders (main 3.0 step 5).
type OrderPortHandler struct {
	svc    *orderport.Service
	logger zerolog.Logger
	appEnv string
	notes  *notify.Recorder // dev only
}

// SetDev turns on the dev-only test hooks (appEnv "dev").
func (h *OrderPortHandler) SetDev(appEnv string, notes *notify.Recorder) {
	h.appEnv, h.notes = appEnv, notes
}

func (h *OrderPortHandler) dev() bool { return h.appEnv == "dev" }

// POST /admin/v1/dev/placements/sweep (dev only) — one sweep now, nothing stale-aged: {resumed, completed, …}.
func (h *OrderPortHandler) DevSweep(c *echo.Context) error {
	if !h.dev() {
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	}
	return c.JSON(http.StatusOK, h.svc.Sweep(c.Request().Context(), 0))
}

// POST /admin/v1/dev/placements/orphan {channel_id, order, charge_cents, hold} (dev only) —
// a pending placement as a main that died mid-work leaves it → {hold_ref}.
func (h *OrderPortHandler) DevOrphan(c *echo.Context) error {
	if !h.dev() {
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	}
	var b struct {
		ChannelID   uuid.UUID         `json:"channel_id"`
		Order       orderport.Request `json:"order"`
		ChargeCents int64             `json:"charge_cents"`
		Hold        bool              `json:"hold"`
	}
	if err := echo.BindBody(c, &b); err != nil {
		return c.JSON(http.StatusBadRequest, errCode("bad body", "bad_request"))
	}
	ref, err := h.svc.DevOrphan(c.Request().Context(), b.ChannelID, b.Order, b.ChargeCents, b.Hold)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errCode(err.Error(), "bad_request"))
	}
	return c.JSON(http.StatusOK, map[string]string{"hold_ref": ref})
}

// GET /admin/v1/dev/notices?location_id= (dev only) — the owner notices main sent.
func (h *OrderPortHandler) DevNotices(c *echo.Context) error {
	id, err := uuid.Parse(c.QueryParam("location_id"))
	if !h.dev() || h.notes == nil || err != nil {
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	}
	return c.JSON(http.StatusOK, map[string]any{"notices": h.notes.For(id)})
}

func NewOrderPortHandler(svc *orderport.Service, logger zerolog.Logger) *OrderPortHandler {
	return &OrderPortHandler{svc: svc, logger: logger.With().Str("module", "orderport-handler").Logger()}
}

func (h *OrderPortHandler) fail(c *echo.Context, err error) error {
	var pend *orderport.Pending
	if errors.As(err, &pend) {
		// main is still at work (DURABILITY-PLAN.md): ask GET /orders/by-key/{key}.
		return c.JSON(http.StatusAccepted, map[string]string{"state": "pending", "client_order_id": pend.ClientOrderID})
	}
	var e *orderport.Error
	if errors.As(err, &e) {
		body := map[string]any{"error": e.Message, "code": e.Code}
		if e.Issues != nil {
			body["issues"] = e.Issues
		}
		return c.JSON(e.Status, body)
	}
	h.logger.Error().Err(err).Msg("channel order")
	return c.JSON(http.StatusInternalServerError, errCode("the order couldn't be placed — try again", "internal"))
}

// POST /channel/v1/orders — 201 a new order, 200 the same order again (the
// same client_order_id).
func (h *OrderPortHandler) Place(c *echo.Context) error {
	ch, ok := appMiddleware.ChannelFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, errCode("missing channel key", "unauthorized"))
	}
	var req orderport.Request
	if err := echo.BindBody(c, &req); err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid order", "bad_request"))
	}
	if op, ok := appMiddleware.ChannelOperatorFromContext(c.Request().Context()); ok {
		req.TakenBy = &op
	}
	if cu, ok := appMiddleware.CustomerFromContext(c.Request().Context()); ok {
		req.Account = &cu // a signed-in website customer (X-Customer-Session)
	}
	ctx := c.Request().Context()
	if h.dev() { // tests: answer 202 sooner (?wait_ms=)
		if ms, err := strconv.Atoi(c.QueryParam("wait_ms")); err == nil && ms > 0 {
			ctx = orderport.WithPlaceWait(ctx, time.Duration(ms)*time.Millisecond)
		}
	}
	v, created, err := h.svc.Place(ctx, ch, req)
	if err != nil {
		return h.fail(c, err)
	}
	if created {
		return c.JSON(http.StatusCreated, v)
	}
	return c.JSON(http.StatusOK, v)
}

// GET /channel/v1/orders/by-key/:key — a placement's state (DURABILITY-PLAN.md):
// {state: pending|placed|refused|expired|voided, order?, error?, expires_at}.
func (h *OrderPortHandler) ByKey(c *echo.Context) error {
	ch, ok := appMiddleware.ChannelFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, errCode("missing channel key", "unauthorized"))
	}
	v, err := h.svc.ByKey(c.Request().Context(), ch, c.Param("key"))
	if errors.Is(err, orderport.ErrNotFound) {
		return c.JSON(http.StatusNotFound, errCode("main has no order under that client_order_id", "unknown_key"))
	}
	if err != nil {
		return h.fail(c, err)
	}
	return c.JSON(http.StatusOK, v)
}

// GET /channel/v1/orders/:id — one of this channel's orders.
func (h *OrderPortHandler) Get(c *echo.Context) error {
	ch, ok := appMiddleware.ChannelFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, errCode("missing channel key", "unauthorized"))
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusNotFound, errCode("order not found", "not_found"))
	}
	v, err := h.svc.Get(c.Request().Context(), ch, id)
	if errors.Is(err, orderport.ErrNotFound) {
		return c.JSON(http.StatusNotFound, errCode("order not found", "not_found"))
	}
	if err != nil {
		return h.fail(c, err)
	}
	return c.JSON(http.StatusOK, v)
}

// GET /channel/v1/orders?before=RFC3339&limit=50 — this channel's orders, newest first.
func (h *OrderPortHandler) List(c *echo.Context) error {
	ch, ok := appMiddleware.ChannelFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, errCode("missing channel key", "unauthorized"))
	}
	before := time.Now().Add(time.Minute)
	if s := c.QueryParam("before"); s != "" {
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return c.JSON(http.StatusBadRequest, errCode("before must be an RFC 3339 time", "bad_request"))
		}
		before = t
	}
	limit := 50
	if s := c.QueryParam("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 200 {
			return c.JSON(http.StatusBadRequest, errCode("limit must be 1–200", "bad_request"))
		}
		limit = n
	}
	out, err := h.svc.List(c.Request().Context(), ch, before, limit)
	if err != nil {
		return h.fail(c, err)
	}
	return c.JSON(http.StatusOK, out)
}
