package handler

import (
	"errors"
	"net/http"

	pricingApp "quiccpos/main/internal/app/pricing"
	"quiccpos/main/internal/domain/pricing"
	"quiccpos/main/internal/infra/database/repositories"
	appMiddleware "quiccpos/main/internal/transport/middleware"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// PricingHandler serves pricing (main 3.0 step 4): the manager's
// /store/v1/me/{pricing,deals,quote} (session) and the channels'
// /channel/v1/{quote,pricing} (channel key, quote permission).
type PricingHandler struct {
	svc    *pricingApp.Service
	logger zerolog.Logger
}

func NewPricingHandler(svc *pricingApp.Service, logger zerolog.Logger) *PricingHandler {
	return &PricingHandler{svc: svc, logger: logger.With().Str("module", "pricing-handler").Logger()}
}

func (h *PricingHandler) pErr(c *echo.Context, err error) error {
	switch {
	case errors.Is(err, pricingApp.ErrNotFound):
		return c.JSON(http.StatusNotFound, errCode(err.Error(), "not_found"))
	case errors.Is(err, repositories.ErrCodeTaken):
		return c.JSON(http.StatusConflict, errCode(err.Error(), "code_taken"))
	case errors.Is(err, pricingApp.ErrBadKind), errors.Is(err, pricingApp.ErrNotTheStore),
		errors.Is(err, pricing.ErrBadSettings), errors.Is(err, pricing.ErrBadDeal):
		return c.JSON(http.StatusBadRequest, errCode(err.Error(), "bad_request"))
	}
	h.logger.Error().Err(err).Msg("pricing")
	return c.JSON(http.StatusInternalServerError, errCode("pricing failed", "internal"))
}

// pricingBody is the manager's pricing settings: the store's and each kind's
// override (a missing kind = the store's sets).
type pricingBody struct {
	Store pricing.StoreSettings           `json:"store"`
	Kinds map[string]pricing.KindOverride `json:"kinds"`
}

// GET /store/v1/me/pricing
func (h *PricingHandler) MePricing(c *echo.Context) error {
	_, lid, _ := meScope(c)
	st, over, err := h.svc.Settings(c.Request().Context(), lid)
	if err != nil {
		return h.pErr(c, err)
	}
	if over == nil {
		over = map[string]pricing.KindOverride{}
	}
	return c.JSON(http.StatusOK, pricingBody{Store: st, Kinds: over})
}

// PUT /store/v1/me/pricing {store, kinds}
func (h *PricingHandler) MeSavePricing(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	var req pricingBody
	if err := echo.BindBody(c, &req); err != nil {
		return badBody(c)
	}
	if req.Kinds == nil {
		req.Kinds = map[string]pricing.KindOverride{}
	}
	if err := h.svc.SaveSettings(c.Request().Context(), tid, lid, req.Store, req.Kinds); err != nil {
		return h.pErr(c, err)
	}
	return h.MePricing(c)
}

func (h *PricingHandler) MeDeals(c *echo.Context) error {
	_, lid, _ := meScope(c)
	out, err := h.svc.Deals(c.Request().Context(), lid)
	if err != nil {
		return h.pErr(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

func (h *PricingHandler) MeCreateDeal(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	var d pricing.StoreDeal
	if err := echo.BindBody(c, &d); err != nil {
		return badBody(c)
	}
	out, err := h.svc.CreateDeal(c.Request().Context(), tid, lid, d)
	if err != nil {
		return h.pErr(c, err)
	}
	return c.JSON(http.StatusCreated, out)
}

// PUT /store/v1/me/deals/:id — replaces the deal.
func (h *PricingHandler) MeUpdateDeal(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	var d pricing.StoreDeal
	if err := echo.BindBody(c, &d); err != nil {
		return badBody(c)
	}
	out, err := h.svc.UpdateDeal(c.Request().Context(), lid, id, d)
	if err != nil {
		return h.pErr(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

func (h *PricingHandler) MeDeleteDeal(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	if err := h.svc.DeleteDeal(c.Request().Context(), lid, id); err != nil {
		return h.pErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// GET /store/v1/me/pricing/context?kind=web — what that kind's default
// view prices with (the editor's previews run the TS engine on it).
func (h *PricingHandler) MeContext(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	pc, _, err := h.svc.Context(c.Request().Context(), tid, lid, c.QueryParam("kind"), nil)
	if err != nil {
		return h.pErr(c, err)
	}
	return c.JSON(http.StatusOK, pc)
}

// POST /store/v1/me/quote?kind=web {cart} — price a cart as a channel of
// that kind would (its kind's default view).
func (h *PricingHandler) MeQuote(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	var cart pricing.Cart
	if err := echo.BindBody(c, &cart); err != nil {
		return badBody(c)
	}
	q, err := h.svc.Quote(c.Request().Context(), tid, lid, c.QueryParam("kind"), nil, cart)
	if err != nil {
		return h.pErr(c, err)
	}
	return c.JSON(http.StatusOK, q)
}

// --- /channel/v1 -----------------------------------------------------------------

func quoteChannel(c *echo.Context) (bool, error) {
	ch, ok := appMiddleware.ChannelFromContext(c.Request().Context())
	if !ok {
		return false, c.JSON(http.StatusUnauthorized, errCode("missing channel key", "unauthorized"))
	}
	if !ch.Permissions.Quote {
		return false, c.JSON(http.StatusForbidden, errCode("this channel may not price carts", "forbidden"))
	}
	return true, nil
}

// channelContext is the pricing context a channel previews with: everything
// but the menu (read it from /channel/v1/menu, same version).
type channelContext struct {
	MenuVersion int64                        `json:"menu_version"`
	Items       map[string]pricing.ItemFacts `json:"items"`
	Settings    pricing.Settings             `json:"settings"`
	Deals       []pricing.Deal               `json:"deals"`
	Timezone    string                       `json:"timezone"`
	Types       pricing.Types                `json:"types"`
}

// GET /channel/v1/pricing
func (h *PricingHandler) ChannelPricing(c *echo.Context) error {
	if ok, err := quoteChannel(c); !ok {
		return err
	}
	ch, _ := appMiddleware.ChannelFromContext(c.Request().Context())
	pc, ver, err := h.svc.Context(c.Request().Context(), ch.TenantID, ch.LocationID, string(ch.Kind), ch.ViewID)
	if err != nil {
		return h.chErr(c, err)
	}
	return c.JSON(http.StatusOK, channelContext{MenuVersion: ver, Items: pc.Items, Settings: pc.Settings, Deals: pc.Deals,
		Timezone: pc.Timezone, Types: pc.Types})
}

// POST /channel/v1/quote {cart}
func (h *PricingHandler) ChannelQuote(c *echo.Context) error {
	if ok, err := quoteChannel(c); !ok {
		return err
	}
	ch, _ := appMiddleware.ChannelFromContext(c.Request().Context())
	var cart pricing.Cart
	if err := echo.BindBody(c, &cart); err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid cart", "bad_request"))
	}
	q, err := h.svc.Quote(c.Request().Context(), ch.TenantID, ch.LocationID, string(ch.Kind), ch.ViewID, cart)
	if err != nil {
		return h.chErr(c, err)
	}
	return c.JSON(http.StatusOK, q)
}

func (h *PricingHandler) chErr(c *echo.Context, err error) error {
	h.logger.Error().Err(err).Msg("channel pricing")
	return c.JSON(http.StatusInternalServerError, errCode("pricing is unavailable", "internal"))
}
