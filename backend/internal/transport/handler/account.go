package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"quiccpos/main/internal/app/customers"
	"quiccpos/main/internal/app/orderport"
	"quiccpos/main/internal/app/receipts"
	channelDom "quiccpos/main/internal/domain/channel"
	"quiccpos/main/internal/domain/customer"
	"quiccpos/main/internal/infra/mail"
	appMiddleware "quiccpos/main/internal/transport/middleware"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// AccountHandler serves a store's customer accounts on the channel API
// (weborder stage 2: the website's sign-in, profile, addresses, saved cards,
// order history, reorder) plus the store's receipt reply-to email and the
// dev hooks for tests (the fake code / fake mail).
type AccountHandler struct {
	svc      *customers.Service
	orders   *orderport.Service
	receipts *receipts.Service
	fakeCode *customers.FakeCodes
	fakeMail *mail.Fake
	appEnv   string
	logger   zerolog.Logger
}

func NewAccountHandler(svc *customers.Service, orders *orderport.Service, rc *receipts.Service, fakeCode *customers.FakeCodes,
	fakeMail *mail.Fake, appEnv string, logger zerolog.Logger) *AccountHandler {
	return &AccountHandler{svc: svc, orders: orders, receipts: rc, fakeCode: fakeCode, fakeMail: fakeMail, appEnv: appEnv,
		logger: logger.With().Str("module", "account-handler").Logger()}
}

func (h *AccountHandler) aErr(c *echo.Context, err error) error {
	switch {
	case errors.Is(err, customer.ErrBadPhone):
		return c.JSON(http.StatusBadRequest, errCode(err.Error(), "bad_phone"))
	case errors.Is(err, customer.ErrBadAddress), errors.Is(err, customer.ErrBadEmail), errors.Is(err, customer.ErrTooLong),
		errors.Is(err, customers.ErrBadInput):
		return c.JSON(http.StatusBadRequest, errCode(err.Error(), "bad_request"))
	case errors.Is(err, customer.ErrMaxAddress):
		return c.JSON(http.StatusConflict, errCode(err.Error(), "too_many_addresses"))
	case errors.Is(err, customer.ErrTooMany):
		return c.JSON(http.StatusTooManyRequests, errCode(err.Error(), "too_many"))
	case errors.Is(err, customer.ErrCallTooSoon):
		return c.JSON(http.StatusTooManyRequests, errCode(err.Error(), "call_too_soon"))
	case errors.Is(err, customer.ErrBadCode):
		return c.JSON(http.StatusUnprocessableEntity, errCode(err.Error(), "bad_code"))
	case errors.Is(err, customer.ErrNotFound), errors.Is(err, orderport.ErrNotFound):
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	case errors.Is(err, customers.ErrDisabled):
		return c.JSON(http.StatusServiceUnavailable, errCode(err.Error(), "signin_disabled"))
	case errors.Is(err, customers.ErrSendFailed):
		return c.JSON(http.StatusBadGateway, errCode(err.Error(), "send_failed"))
	}
	h.logger.Error().Err(err).Msg("account")
	return c.JSON(http.StatusServiceUnavailable, errCode("try again", "try_again"))
}

// webChannel is the request's channel when it is a website (accounts are
// the website's only).
// A nil channel means the refusal was written: return the error as is.
func webChannel(c *echo.Context) (*channelDom.Channel, error) {
	ch, ok := appMiddleware.ChannelFromContext(c.Request().Context())
	if !ok {
		return nil, c.JSON(http.StatusUnauthorized, errCode("missing channel key", "unauthorized"))
	}
	if ch.Kind != channelDom.KindWeb {
		return nil, c.JSON(http.StatusForbidden, errCode("customer accounts are for the store's website", "forbidden"))
	}
	return &ch, nil
}

// callerIP is the customer's IP as the storefront forwards it
// (X-Customer-IP), else the caller's.
func callerIP(c *echo.Context) string {
	if ip := strings.TrimSpace(c.Request().Header.Get("X-Customer-IP")); ip != "" && len(ip) <= 45 {
		return ip
	}
	return c.RealIP()
}

// POST /channel/v1/account/code {phone, channel sms|call} → 202 {sent_to, channel, call_after_s}
func (h *AccountHandler) SendCode(c *echo.Context) error {
	ch, err := webChannel(c)
	if ch == nil {
		return err
	}
	var body struct {
		Phone   string `json:"phone"`
		Channel string `json:"channel"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid JSON body", "bad_request"))
	}
	res, err := h.svc.SendCode(c.Request().Context(), ch.LocationID, body.Phone, body.Channel, callerIP(c))
	if err != nil {
		return h.aErr(c, err)
	}
	return c.JSON(http.StatusAccepted, res)
}

// POST /channel/v1/account/verify {phone, code, name?} → {session, expires_at, customer, new, joined_orders}
func (h *AccountHandler) Verify(c *echo.Context) error {
	ch, err := webChannel(c)
	if ch == nil {
		return err
	}
	var body struct {
		Phone string `json:"phone"`
		Code  string `json:"code"`
		Name  string `json:"name"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid JSON body", "bad_request"))
	}
	res, err := h.svc.Verify(c.Request().Context(), ch.TenantID, ch.LocationID, body.Phone, body.Code, body.Name, callerIP(c))
	if err != nil {
		return h.aErr(c, err)
	}
	return c.JSON(http.StatusOK, res)
}

func signedIn(c *echo.Context) (customer.Customer, bool) {
	return appMiddleware.CustomerFromContext(c.Request().Context())
}

// GET /channel/v1/account
func (h *AccountHandler) Get(c *echo.Context) error {
	cu, _ := signedIn(c)
	a, err := h.svc.Account(c.Request().Context(), cu)
	if err != nil {
		return h.aErr(c, err)
	}
	return c.JSON(http.StatusOK, a)
}

// PATCH /channel/v1/account {name?, email?}
func (h *AccountHandler) Update(c *echo.Context) error {
	cu, _ := signedIn(c)
	var body struct {
		Name  *string `json:"name"`
		Email *string `json:"email"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid JSON body", "bad_request"))
	}
	if _, err := h.svc.UpdateProfile(c.Request().Context(), cu, body.Name, body.Email); err != nil {
		return h.aErr(c, err)
	}
	updated, err := h.svc.Authenticate(c.Request().Context(), cu.LocationID, c.Request().Header.Get("X-Customer-Session"))
	if err != nil {
		return h.aErr(c, err)
	}
	a, err := h.svc.Account(c.Request().Context(), updated)
	if err != nil {
		return h.aErr(c, err)
	}
	return c.JSON(http.StatusOK, a)
}

// DELETE /channel/v1/account — the customer deletes their account.
func (h *AccountHandler) Delete(c *echo.Context) error {
	cu, _ := signedIn(c)
	if err := h.svc.Delete(c.Request().Context(), cu); err != nil {
		return h.aErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// POST /channel/v1/account/logout
func (h *AccountHandler) Logout(c *echo.Context) error {
	if err := h.svc.SignOut(c.Request().Context(), c.Request().Header.Get("X-Customer-Session")); err != nil {
		return h.aErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

type addressBody struct {
	Label   string `json:"label"`
	Line1   string `json:"line1"`
	Line2   string `json:"line2"`
	City    string `json:"city"`
	State   string `json:"state"`
	Zip     string `json:"zip"`
	Notes   string `json:"notes"`
	Default bool   `json:"default"`
}

func (b addressBody) address() customer.Address {
	return customer.Address{Label: b.Label, Line1: b.Line1, Line2: b.Line2, City: b.City, State: b.State, Zip: b.Zip, Notes: b.Notes, Default: b.Default}
}

// POST /channel/v1/account/addresses
func (h *AccountHandler) AddAddress(c *echo.Context) error {
	cu, _ := signedIn(c)
	var body addressBody
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid JSON body", "bad_request"))
	}
	a, err := h.svc.SaveAddress(c.Request().Context(), cu, nil, body.address())
	if err != nil {
		return h.aErr(c, err)
	}
	return c.JSON(http.StatusCreated, a)
}

// PUT /channel/v1/account/addresses/:id
func (h *AccountHandler) PutAddress(c *echo.Context) error {
	cu, _ := signedIn(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	}
	var body addressBody
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid JSON body", "bad_request"))
	}
	a, err := h.svc.SaveAddress(c.Request().Context(), cu, &id, body.address())
	if err != nil {
		return h.aErr(c, err)
	}
	return c.JSON(http.StatusOK, a)
}

// DELETE /channel/v1/account/addresses/:id
func (h *AccountHandler) DeleteAddress(c *echo.Context) error {
	cu, _ := signedIn(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	}
	if err := h.svc.DeleteAddress(c.Request().Context(), cu, id); err != nil {
		return h.aErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// DELETE /channel/v1/account/cards/:id — forget a saved card (and turn it off at Square).
func (h *AccountHandler) DeleteCard(c *echo.Context) error {
	cu, _ := signedIn(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	}
	if err := h.svc.DeleteCard(c.Request().Context(), cu, id); err != nil {
		return h.aErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// GET /channel/v1/account/orders?before=&limit= — the account's orders, newest first.
func (h *AccountHandler) Orders(c *echo.Context) error {
	ch, err := webChannel(c)
	if ch == nil {
		return err
	}
	cu, _ := signedIn(c)
	before := time.Now().Add(time.Minute)
	if b := c.QueryParam("before"); b != "" {
		t, err := time.Parse(time.RFC3339Nano, b)
		if err != nil {
			return c.JSON(http.StatusBadRequest, errCode("before must be an RFC 3339 time", "bad_request"))
		}
		before = t
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	links, err := h.svc.OrderLinks(c.Request().Context(), cu, before, limit)
	if err != nil {
		return h.aErr(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"orders": h.orders.ForLinks(c.Request().Context(), *ch, links)})
}

// POST /channel/v1/account/orders/:id/reorder {type?} → a cart re-priced today.
func (h *AccountHandler) Reorder(c *echo.Context) error {
	ch, err := webChannel(c)
	if ch == nil {
		return err
	}
	cu, _ := signedIn(c)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	}
	var body struct {
		Type string `json:"type"`
	}
	_ = c.Bind(&body)
	link, err := h.svc.OrderLink(c.Request().Context(), cu, id)
	if err != nil {
		return h.aErr(c, err)
	}
	res, err := h.orders.Reorder(c.Request().Context(), *ch, link, body.Type)
	if err != nil {
		return h.aErr(c, err)
	}
	return c.JSON(http.StatusOK, res)
}

// --- the store manager: the receipts' reply-to email --------------------------------------

// GET /store/v1/me/site/contact
func (h *AccountHandler) MeContact(c *echo.Context) error {
	_, lid, _ := meScope(c)
	e, err := h.svc.ReplyEmail(c.Request().Context(), lid)
	if err != nil {
		return h.aErr(c, err)
	}
	return c.JSON(http.StatusOK, map[string]string{"reply_email": e})
}

// PUT /store/v1/me/site/contact {reply_email} ("" = receipts have no reply address)
func (h *AccountHandler) MePutContact(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	var body struct {
		ReplyEmail string `json:"reply_email"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid JSON body", "bad_request"))
	}
	if err := h.svc.SetReplyEmail(c.Request().Context(), tid, lid, body.ReplyEmail); err != nil {
		return h.aErr(c, err)
	}
	return h.MeContact(c)
}

// --- admin -----------------------------------------------------------------------------

// GET /admin/v1/orders/:id/receipts — an order's email receipts.
func (h *AccountHandler) AdminReceipts(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid order id", "bad_request"))
	}
	rs, err := h.receipts.ForOrder(c.Request().Context(), id)
	if err != nil {
		return h.aErr(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"receipts": rs})
}

func (h *AccountHandler) devOnly(c *echo.Context) bool { return h.appEnv == "dev" }

// GET /admin/v1/dev/signin-code?phone= (dev only, the fake) → {code, channels}
func (h *AccountHandler) DevCode(c *echo.Context) error {
	if !h.devOnly(c) || h.fakeCode == nil {
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	}
	p := customers.NormPhone(c.QueryParam("phone"))
	code, channels := h.fakeCode.Last(customer.E164(p))
	if channels == nil {
		channels = []string{}
	}
	return c.JSON(http.StatusOK, map[string]any{"code": code, "channels": channels})
}

// GET /admin/v1/dev/mail?to= (dev only, the fake) → {messages}
func (h *AccountHandler) DevMail(c *echo.Context) error {
	if !h.devOnly(c) || h.fakeMail == nil {
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	}
	ms := h.fakeMail.To(c.QueryParam("to"))
	if ms == nil {
		ms = []mail.Sent{}
	}
	return c.JSON(http.StatusOK, map[string]any{"messages": ms})
}

// POST /admin/v1/dev/receipts/tick (dev only) — send what is queued now.
func (h *AccountHandler) DevReceiptsTick(c *echo.Context) error {
	if !h.devOnly(c) {
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	}
	return c.JSON(http.StatusOK, map[string]int{"worked": h.receipts.Tick(c.Request().Context())})
}
