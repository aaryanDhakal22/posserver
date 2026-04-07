package handler

import (
	"errors"
	"net/http"
	"strconv"

	orderSvc "quiccpos/main/internal/app/order"
	"quiccpos/main/internal/transport/dto"

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

// POST /orders
func (h *OrderHandler) Create(c *echo.Context) error {
	var req dto.Order
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid request body"))
	}
	o := req.ToDomain()
	if err := h.svc.Create(c.Request().Context(), &o); err != nil {
		h.logger.Error().Err(err).Msg("failed to create order")
		return c.JSON(http.StatusInternalServerError, errResp("failed to create order"))
	}
	return c.JSON(http.StatusCreated, dto.FromDomain(o))
}

// GET /orders?offset=x&num=y
func (h *OrderHandler) GetOrders(c *echo.Context) error {
	offset, err := queryInt(c, "offset", 0)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("offset must be an integer"))
	}
	num, err := queryInt(c, "num", 20)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("num must be an integer"))
	}

	orders, err := h.svc.GetOrdersPage(c.Request().Context(), offset, num)
	if err != nil {
		h.logger.Error().Err(err).Msg("failed to get orders")
		return c.JSON(http.StatusInternalServerError, errResp("failed to get orders"))
	}

	resp := make([]dto.Order, 0, len(orders))
	for _, o := range orders {
		resp = append(resp, dto.FromDomain(o))
	}
	return c.JSON(http.StatusOK, resp)
}

// GET /orders/latest
func (h *OrderHandler) GetLatest(c *echo.Context) error {
	o, err := h.svc.GetLatest(c.Request().Context())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errResp("no orders found"))
		}
		h.logger.Error().Err(err).Msg("failed to get latest order")
		return c.JSON(http.StatusInternalServerError, errResp("failed to get latest order"))
	}
	return c.JSON(http.StatusOK, dto.FromDomain(*o))
}

// GET /orders/:id
func (h *OrderHandler) GetByID(c *echo.Context) error {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be an integer"))
	}
	o, err := h.svc.GetByID(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return c.JSON(http.StatusNotFound, errResp("order not found"))
		}
		h.logger.Error().Err(err).Msg("failed to get order")
		return c.JSON(http.StatusInternalServerError, errResp("failed to get order"))
	}
	return c.JSON(http.StatusOK, dto.FromDomain(*o))
}

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

func errResp(msg string) map[string]string { return map[string]string{"error": msg} }

func queryInt(c *echo.Context, key string, defaultVal int) (int, error) {
	raw := c.QueryParam(key)
	if raw == "" {
		return defaultVal, nil
	}
	return strconv.Atoi(raw)
}
