package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	orderSvc "quiccpos/main/internal/app/order"
	"quiccpos/main/internal/domain/order"
	appMiddleware "quiccpos/main/internal/transport/middleware"
	sseTransport "quiccpos/main/internal/transport/sse"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

type OrderHandler struct {
	svc    *orderSvc.Service
	logger zerolog.Logger
}

func NewOrderHandler(svc *orderSvc.Service, logger zerolog.Logger) *OrderHandler {
	return &OrderHandler{
		svc:    svc,
		logger: logger.With().Str("module", "order-handler").Logger(),
	}
}

// GET /orders?offset=x&num=y&q=&status=&from=&to=&sort= — scoped to caller's
// (tenant, location). q matches customer name / source_event_id; status is an
// exact match; from/to are RFC3339 bounds on received_at; sort=asc reverses the
// default newest-first order. All filters are optional.
func (h *OrderHandler) GetOrders(c *echo.Context) error {
	log := zerolog.Ctx(c.Request().Context()).With().Str("handler", "GetOrders").Logger()
	scope := appMiddleware.ScopeFromContext(c.Request().Context())

	offset, err := queryInt(c, "offset", 0)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("offset must be an integer"))
	}
	num, err := queryInt(c, "num", 20)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("num must be an integer"))
	}
	if num < 1 || num > 200 {
		return c.JSON(http.StatusBadRequest, errResp("num must be 1-200"))
	}
	if offset < 0 {
		return c.JSON(http.StatusBadRequest, errResp("offset must be >= 0"))
	}

	from, err := queryTime(c, "from")
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("from must be an RFC3339 timestamp"))
	}
	to, err := queryTime(c, "to")
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("to must be an RFC3339 timestamp"))
	}

	filter := order.OrderFilter{
		Query:   c.QueryParam("q"),
		Status:  c.QueryParam("status"),
		From:    from,
		To:      to,
		Reverse: c.QueryParam("sort") == "asc",
		Offset:  offset,
		Limit:   num,
	}

	orders, err := h.svc.SearchOrders(c.Request().Context(), scope, filter)
	if err != nil {
		log.Error().Err(err).Msg("failed to get orders page")
		return c.JSON(http.StatusInternalServerError, errResp("failed to get orders"))
	}

	resp, err := buildEvents(c.Request().Context(), orders)
	if err != nil {
		log.Error().Err(err).Msg("failed to build events")
		return c.JSON(http.StatusInternalServerError, errResp("failed to encode orders"))
	}
	return c.JSON(http.StatusOK, resp)
}

// GET /orders/:id — scoped to caller's (tenant, location).
func (h *OrderHandler) GetByID(c *echo.Context) error {
	log := zerolog.Ctx(c.Request().Context()).With().Str("handler", "GetByID").Logger()
	scope := appMiddleware.ScopeFromContext(c.Request().Context())

	parsed, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}

	o, err := h.svc.GetByID(c.Request().Context(), scope, order.ID(parsed))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errResp("order not found"))
		}
		log.Error().Err(err).Str("order_id", parsed.String()).Msg("failed to get order")
		return c.JSON(http.StatusInternalServerError, errResp("failed to get order"))
	}
	return jsonEvent(c, o)
}

// GET /orders/by-source/:source/:source_event_id — scoped to caller.
func (h *OrderHandler) GetBySourceEvent(c *echo.Context) error {
	log := zerolog.Ctx(c.Request().Context()).With().Str("handler", "GetBySourceEvent").Logger()
	scope := appMiddleware.ScopeFromContext(c.Request().Context())

	src := order.Source(c.Param("source"))
	eventID := c.Param("source_event_id")
	if src == "" || eventID == "" {
		return c.JSON(http.StatusBadRequest, errResp("source and source_event_id required"))
	}

	o, err := h.svc.FindBySourceEvent(c.Request().Context(), scope, src, eventID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errResp("order not found"))
		}
		log.Error().Err(err).Str("source", string(src)).Str("event_id", eventID).Msg("lookup failed")
		return c.JSON(http.StatusInternalServerError, errResp("failed to get order"))
	}
	return jsonEvent(c, o)
}

func errResp(msg string) map[string]string { return map[string]string{"error": msg} }

// errCode is errResp plus a stable machine-readable `code` so clients (the agent
// UI) can branch on the failure kind without string-matching the message.
func errCode(msg, code string) map[string]string {
	return map[string]string{"error": msg, "code": code}
}

func queryInt(c *echo.Context, key string, defaultVal int) (int, error) {
	raw := c.QueryParam(key)
	if raw == "" {
		return defaultVal, nil
	}
	return strconv.Atoi(raw)
}

// queryTime parses an optional RFC3339 query param. Empty → nil (no bound).
func queryTime(c *echo.Context, key string) (*time.Time, error) {
	raw := c.QueryParam(key)
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func jsonEvent(c *echo.Context, o *order.Order) error {
	ev := sseTransport.EventFromSnapshot(c.Request().Context(), o.Snapshot(), false)
	return c.JSON(http.StatusOK, ev)
}

func buildEvents(ctx context.Context, orders []*order.Order) ([]sseTransport.OrderEvent, error) {
	out := make([]sseTransport.OrderEvent, 0, len(orders))
	for _, o := range orders {
		out = append(out, sseTransport.EventFromSnapshot(ctx, o.Snapshot(), false))
	}
	return out, nil
}
