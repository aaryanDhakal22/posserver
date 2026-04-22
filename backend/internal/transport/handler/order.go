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
	log := zerolog.Ctx(c.Request().Context()).With().Str("handler", "Create").Logger()
	log.Debug().Msg("binding request body")

	var req dto.Order
	if err := c.Bind(&req); err != nil {
		log.Warn().Err(err).Msg("failed to bind request body")
		return c.JSON(http.StatusBadRequest, errResp("invalid request body"))
	}
	log.Debug().Int("order_id", req.OrderID).Msg("request bound")

	o := req.ToDomain()
	if err := h.svc.Create(c.Request().Context(), &o); err != nil {
		log.Error().Err(err).Int("order_id", o.OrderID).Msg("failed to create order")
		return c.JSON(http.StatusInternalServerError, errResp("failed to create order"))
	}

	log.Info().Int("order_id", o.OrderID).Msg("order created")
	return c.JSON(http.StatusCreated, dto.FromDomain(o))
}

// GET /orders?offset=x&num=y
func (h *OrderHandler) GetOrders(c *echo.Context) error {
	log := zerolog.Ctx(c.Request().Context()).With().Str("handler", "GetOrders").Logger()

	offset, err := queryInt(c, "offset", 0)
	if err != nil {
		log.Warn().Str("raw_offset", c.QueryParam("offset")).Msg("invalid offset param")
		return c.JSON(http.StatusBadRequest, errResp("offset must be an integer"))
	}
	num, err := queryInt(c, "num", 20)
	if err != nil {
		log.Warn().Str("raw_num", c.QueryParam("num")).Msg("invalid num param")
		return c.JSON(http.StatusBadRequest, errResp("num must be an integer"))
	}
	log.Debug().Int("offset", offset).Int("num", num).Msg("fetching orders page")

	orders, err := h.svc.GetOrdersPage(c.Request().Context(), offset, num)
	if err != nil {
		log.Error().Err(err).Int("offset", offset).Int("num", num).Msg("failed to get orders page")
		return c.JSON(http.StatusInternalServerError, errResp("failed to get orders"))
	}

	resp := make([]dto.Order, 0, len(orders))
	for _, o := range orders {
		resp = append(resp, dto.FromDomain(o))
	}
	log.Info().Int("offset", offset).Int("num", num).Int("returned", len(resp)).Msg("orders page returned")
	return c.JSON(http.StatusOK, resp)
}

// GET /orders/latest
func (h *OrderHandler) GetLatest(c *echo.Context) error {
	log := zerolog.Ctx(c.Request().Context()).With().Str("handler", "GetLatest").Logger()
	log.Debug().Msg("fetching latest order")

	o, err := h.svc.GetLatest(c.Request().Context())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			log.Info().Msg("no orders found")
			return c.JSON(http.StatusNotFound, errResp("no orders found"))
		}
		log.Error().Err(err).Msg("failed to get latest order")
		return c.JSON(http.StatusInternalServerError, errResp("failed to get latest order"))
	}
	log.Info().Int("order_id", o.OrderID).Msg("latest order returned")
	return c.JSON(http.StatusOK, dto.FromDomain(*o))
}

// GET /orders/:id
func (h *OrderHandler) GetByID(c *echo.Context) error {
	log := zerolog.Ctx(c.Request().Context()).With().Str("handler", "GetByID").Logger()

	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		log.Warn().Str("raw_id", c.Param("id")).Msg("invalid order id param")
		return c.JSON(http.StatusBadRequest, errResp("id must be an integer"))
	}
	log.Debug().Int("order_id", id).Msg("fetching order by id")

	o, err := h.svc.GetByID(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			log.Info().Int("order_id", id).Msg("order not found")
			return c.JSON(http.StatusNotFound, errResp("order not found"))
		}
		log.Error().Err(err).Int("order_id", id).Msg("failed to get order")
		return c.JSON(http.StatusInternalServerError, errResp("failed to get order"))
	}
	log.Info().Int("order_id", id).Msg("order returned")
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
