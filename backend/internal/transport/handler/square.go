package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	cardholdApp "quiccpos/main/internal/app/cardhold"
	"quiccpos/main/internal/app/orderport"
	"quiccpos/main/internal/app/squarepay"
	appMiddleware "quiccpos/main/internal/transport/middleware"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// SquareHandler serves Square (weborder stage 2): a store connects its
// account, Square's OAuth callback and webhooks, the storefront's payment
// setup, and the card holds.
type SquareHandler struct {
	svc    *squarepay.Service
	holds  *cardholdApp.Service
	appEnv string
	logger zerolog.Logger
}

func NewSquareHandler(svc *squarepay.Service, holds *cardholdApp.Service, appEnv string, logger zerolog.Logger) *SquareHandler {
	return &SquareHandler{svc: svc, holds: holds, appEnv: appEnv, logger: logger.With().Str("module", "square-handler").Logger()}
}

func (h *SquareHandler) sErr(c *echo.Context, err error) error {
	switch {
	case errors.Is(err, squarepay.ErrDisabled):
		return c.JSON(http.StatusServiceUnavailable, errCode(err.Error(), "square_disabled"))
	case errors.Is(err, squarepay.ErrNotConnected):
		return c.JSON(http.StatusConflict, errCode(err.Error(), "not_connected"))
	case errors.Is(err, squarepay.ErrUnknownSquare):
		return c.JSON(http.StatusBadRequest, errCode(err.Error(), "bad_request"))
	case errors.Is(err, squarepay.ErrHoldsOpen):
		return c.JSON(http.StatusConflict, errCode(err.Error(), "holds_open"))
	case errors.Is(err, pgx.ErrNoRows):
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	}
	h.logger.Error().Err(err).Msg("square")
	return c.JSON(http.StatusBadGateway, errCode("Square didn't answer — try again", "square_error"))
}

// --- the store manager (Store › Details › Square) ----------------------------------

// GET /store/v1/me/square
func (h *SquareHandler) MeStatus(c *echo.Context) error {
	_, lid, _ := meScope(c)
	st, err := h.svc.Status(c.Request().Context(), lid)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, st)
}

// POST /store/v1/me/square/connect → {url}: send the owner there.
func (h *SquareHandler) MeConnect(c *echo.Context) error {
	_, lid, _ := meScope(c)
	u, err := h.svc.ConnectURL(lid)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, map[string]string{"url": u})
}

// PUT /store/v1/me/square/location {square_location_id}
func (h *SquareHandler) MeSetLocation(c *echo.Context) error {
	_, lid, _ := meScope(c)
	var body struct {
		SquareLocationID string `json:"square_location_id"`
	}
	if err := c.Bind(&body); err != nil || body.SquareLocationID == "" {
		return c.JSON(http.StatusBadRequest, errCode("send {square_location_id}", "bad_request"))
	}
	if err := h.svc.SetLocation(c.Request().Context(), lid, body.SquareLocationID); err != nil {
		return h.sErr(c, err)
	}
	return h.MeStatus(c)
}

// DELETE /store/v1/me/square
func (h *SquareHandler) MeDisconnect(c *echo.Context) error {
	_, lid, _ := meScope(c)
	if err := h.svc.Disconnect(c.Request().Context(), lid); err != nil {
		return h.sErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// GET /store/v1/me/card-holds?status=held|captured|cancelled|failed&limit= —
// recent online card holds with who ordered.
func (h *SquareHandler) MeHolds(c *echo.Context) error {
	_, lid, _ := meScope(c)
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	status := c.QueryParam("status")
	switch status {
	case "", "held", "captured", "cancelled", "failed":
	default:
		return c.JSON(http.StatusBadRequest, errCode("status must be held, captured, cancelled or failed", "bad_request"))
	}
	hs, err := h.holds.Rows(c.Request().Context(), lid, status, limit)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"holds": hs})
}

// POST /store/v1/me/card-holds/:order_id/capture — "Charge now".
func (h *SquareHandler) MeChargeNow(c *echo.Context) error {
	_, lid, _ := meScope(c)
	oid, ok := orderParam(c)
	if !ok {
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	}
	hold, err := h.holds.ChargeNow(c.Request().Context(), lid, oid)
	if errors.Is(err, cardholdApp.ErrNotHeld) {
		return c.JSON(http.StatusConflict, errCode(err.Error(), "not_held"))
	}
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, hold)
}

// --- Square → main ----------------------------------------------------------------

// GET /square/oauth/callback?code=&state= — Square sends the owner back here.
func (h *SquareHandler) OAuthCallback(c *echo.Context) error {
	q := c.Request().URL.Query()
	_, to, err := h.svc.Callback(c.Request().Context(), q.Get("code"), q.Get("state"), q.Get("error"))
	if err != nil {
		h.logger.Warn().Err(err).Msg("square oauth callback")
	}
	return c.Redirect(http.StatusFound, to)
}

// POST /square/oauth/forward {code, state, error} — the storefront relays
// Square's redirect when SQUARE_REDIRECT_URL is its own public
// /square/oauth/callback (main stays private; DURABILITY-PLAN.md step 6).
// PlatformAuth has checked the storefront's key; the signed state names the
// store and nothing is exchanged unless it checks out. Answers {result,
// redirect}: the storefront sends the manager's browser to redirect (Store ›
// Payments with ?square=result). The code is never logged.
func (h *SquareHandler) OAuthForward(c *echo.Context) error {
	var b struct {
		Code  string `json:"code"`
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(c.Request().Body, 8<<10)).Decode(&b); err != nil || b.State == "" || len(b.Code) > 1024 || len(b.State) > 512 || len(b.Error) > 256 {
		return c.JSON(http.StatusBadRequest, errCode("send {code, state} (or {state, error}) from Square's redirect", "bad_request"))
	}
	lid, result, err := h.svc.Finish(c.Request().Context(), b.Code, b.State, b.Error)
	out := map[string]any{"result": result, "redirect": h.svc.ReturnURL(result)}
	status := http.StatusOK
	switch {
	case err == nil, errors.Is(err, squarepay.ErrDenied):
	case errors.Is(err, squarepay.ErrBadState):
		status, out["code"] = http.StatusBadRequest, "bad_state"
	case errors.Is(err, squarepay.ErrDisabled):
		status, out["code"] = http.StatusServiceUnavailable, "square_disabled"
	case errors.Is(err, squarepay.ErrUnknownPlace):
		status, out["code"] = http.StatusNotFound, "not_found"
	default:
		status, out["code"] = http.StatusBadGateway, "square_error"
	}
	if err != nil {
		out["error"] = err.Error()
		h.logger.Warn().Err(err).Str("location_id", lid.String()).Str("result", result).Msg("square oauth forward")
	}
	return c.JSON(status, out)
}

// POST /square/webhook — Square's events (signature checked).
func (h *SquareHandler) Webhook(c *echo.Context) error {
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, 1<<20))
	if err != nil {
		return c.NoContent(http.StatusBadRequest)
	}
	if err := h.svc.VerifyWebhook(c.Request().Header.Get("X-Square-Hmacsha256-Signature"), body); err != nil {
		return c.JSON(http.StatusUnauthorized, errCode(err.Error(), "unauthorized"))
	}
	var ev squarepay.Event
	if err := json.Unmarshal(body, &ev); err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid event", "bad_request"))
	}
	if err := h.svc.Webhook(c.Request().Context(), ev, h.holds.SquarePayment); err != nil {
		h.logger.Error().Err(err).Str("type", ev.Type).Msg("square webhook")
		return c.NoContent(http.StatusInternalServerError) // Square retries
	}
	return c.NoContent(http.StatusOK)
}

// --- the storefront ----------------------------------------------------------------

// GET /channel/v1/payments — how this channel takes payments: the methods it
// may use and, when the store's Square is connected, what the Web Payments
// SDK needs (app id + Square location).
func (h *SquareHandler) ChannelPayments(c *echo.Context) error {
	ch, ok := appMiddleware.ChannelFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, errCode("missing channel key", "unauthorized"))
	}
	out := map[string]any{"methods": ch.Permissions.Payments, "card": nil, "hold_hours": int(orderport.HoldFor.Hours())}
	takesCard := false
	for _, m := range ch.Permissions.Payments {
		takesCard = takesCard || m == "card"
	}
	if takesCard {
		if st, err := h.svc.Status(c.Request().Context(), ch.LocationID); err == nil && st.Connected && st.SquareLocationID != "" {
			out["card"] = map[string]any{"provider": "square", "env": h.svc.Env(), "app_id": h.svc.AppID(), "location_id": st.SquareLocationID}
		}
	}
	return c.JSON(http.StatusOK, out)
}

// --- admin -------------------------------------------------------------------------

func orderParam(c *echo.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("order_id"))
	return id, err == nil
}

// GET /admin/v1/card-holds/:order_id
func (h *SquareHandler) AdminHold(c *echo.Context) error {
	oid, ok := orderParam(c)
	if !ok {
		return c.JSON(http.StatusBadRequest, errCode("invalid order id", "bad_request"))
	}
	hold, err := h.holds.Hold(c.Request().Context(), oid)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, hold)
}

// POST /admin/v1/card-holds/:order_id/capture — capture one hold now.
func (h *SquareHandler) AdminCapture(c *echo.Context) error {
	oid, ok := orderParam(c)
	if !ok {
		return c.JSON(http.StatusBadRequest, errCode("invalid order id", "bad_request"))
	}
	cur, err := h.holds.Hold(c.Request().Context(), oid)
	if err != nil {
		return h.sErr(c, err)
	}
	hold, err := h.holds.CaptureNow(c.Request().Context(), cur.LocationID, oid)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, hold)
}

// POST /admin/v1/card-holds/:order_id/due (dev only) — make the hold's
// safety capture due now and run the worker once (tests can't wait 24 h).
func (h *SquareHandler) AdminDue(c *echo.Context) error {
	if h.appEnv != "dev" {
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	}
	oid, ok := orderParam(c)
	if !ok {
		return c.JSON(http.StatusBadRequest, errCode("invalid order id", "bad_request"))
	}
	if err := h.holds.MakeDue(c.Request().Context(), oid); err != nil {
		return h.sErr(c, err)
	}
	h.holds.Tick(c.Request().Context())
	hold, err := h.holds.Hold(c.Request().Context(), oid)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, hold)
}

// POST /admin/v1/card-holds/tick (dev only) — run the capture worker once.
func (h *SquareHandler) AdminTick(c *echo.Context) error {
	if h.appEnv != "dev" {
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	}
	return c.JSON(http.StatusOK, map[string]int{"worked": h.holds.Tick(c.Request().Context())})
}
