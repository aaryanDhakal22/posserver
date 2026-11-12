// Package orderport is the order port (main 3.0 step 5): a channel places an
// order — checked against the store's status, re-priced with the pricing
// engine, its address checked, paid through the payment port — and main
// builds the domain Order and saves it through today's path (outbox → SSE →
// the Pi), so the agent sees an ordinary Snapshot. Placing is idempotent on
// the channel's client_order_id.
package orderport

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"quiccpos/main/internal/app/geo"
	storestatusApp "quiccpos/main/internal/app/storestatus"
	channelDom "quiccpos/main/internal/domain/channel"
	"quiccpos/main/internal/domain/customer"
	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/notify"
	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/domain/payment"
	"quiccpos/main/internal/domain/pricing"
	"quiccpos/main/internal/infra/database/repositories"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// Error is a refusal a channel can act on: an HTTP status, a stable code,
// a message for people and (cart_invalid) the cart's issues.
type Error struct {
	Status  int
	Code    string
	Message string
	Issues  []pricing.Issue
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func refuse(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

// Pricing builds a channel's pricing context (the pricing service).
type Pricing interface {
	Context(ctx context.Context, tenantID, locationID uuid.UUID, kind string, viewID *uuid.UUID) (*pricing.Context, int64, error)
}

// Status reads the store's status and order times (store status).
type Status interface {
	Status(ctx context.Context, tenantID, locationID uuid.UUID) (*storestatusApp.Full, error)
	Times(ctx context.Context, tenantID, locationID uuid.UUID, kind, date string) ([]time.Time, error)
	DeliveryCheck(ctx context.Context, tenantID, locationID uuid.UUID, a geo.Address) (*storestatusApp.DeliveryCheck, error)
}

// Bank reads the store's bank (bank item names for the kitchen).
type Bank interface {
	Bank(ctx context.Context, locationID uuid.UUID) (*menu.Bank, error)
}

// Router routes bank items to printers (the menu service).
type Router interface {
	RouteBankItems(ctx context.Context, locationID uuid.UUID, ids []uuid.UUID) ([]order.ItemRoute, error)
}

// Orders saves and reads orders (the order service).
type Orders interface {
	Create(ctx context.Context, o *order.Order) error
	GetByID(ctx context.Context, scope order.Scope, id order.ID) (*order.Order, error)
}

// Links reads channel ↔ order links.
type Links interface {
	ByClient(ctx context.Context, channelID uuid.UUID, clientOrderID string) (repositories.ChannelOrder, error)
	Get(ctx context.Context, orderID uuid.UUID) (repositories.ChannelOrder, error)
	List(ctx context.Context, channelID uuid.UUID, before time.Time, limit int) ([]repositories.ChannelOrder, error)
}

type Service struct {
	pricing  Pricing
	status   Status
	bank     Bank
	router   Router
	orders   Orders
	links    Links
	gateways map[string]payment.Gateway
	notifier notify.Notifier // problems → the owner (nil = log only)
	accounts Accounts
	logger   zerolog.Logger
	now      func() time.Time
	pl       placementWiring // durability (DURABILITY-PLAN.md); nil placements = off
}

// NewService: gateways maps a payment method to its adapter; a method with
// none is refused (card until Square is connected — the owner, 2026-11-02).
func NewService(p Pricing, st Status, b Bank, r Router, o Orders, l Links, gateways map[string]payment.Gateway, lg zerolog.Logger) *Service {
	return &Service{pricing: p, status: st, bank: b, router: r, orders: o, links: l, gateways: gateways,
		logger: lg.With().Str("module", "orderport").Logger(), now: time.Now}
}

// HoldFor is how long an online card hold waits for the driver's clock-out
// or Close batch before main captures it by itself (owner, 2026-11-09).
var HoldFor = 24 * time.Hour

// Request is a channel's order.
type Request struct {
	ClientOrderID string         `json:"client_order_id"`
	Type          string         `json:"type"` // pickup | delivery
	Lines         []pricing.Line `json:"lines"`
	Code          string         `json:"code"`
	TipCents      *int64         `json:"tip_cents"`
	TipPct        *int           `json:"tip_pct"`
	FirstOrder    *bool          `json:"first_order"`
	Payment       struct {
		Method            string `json:"method"` // cash | card
		Token             string `json:"token"`
		VerificationToken string `json:"verification_token"` // the buyer's 3-D Secure answer (Square verifyBuyer)
		CardID            string `json:"card_id"`            // a saved card of the signed-in customer (instead of token)
		SaveCard          bool   `json:"save_card"`          // keep this card for next time (signed-in customers)
	} `json:"payment"`
	Customer struct {
		Name  string `json:"name"`
		Phone string `json:"phone"`
		Email string `json:"email"`
	} `json:"customer"`
	Address *struct {
		Line1 string `json:"line1"`
		Line2 string `json:"line2"`
		City  string `json:"city"`
		State string `json:"state"`
		Zip   string `json:"zip"`
		Notes string `json:"notes"`
	} `json:"address"`
	PromisedAt         *time.Time `json:"promised_at"` // nil = as soon as possible
	Notes              string     `json:"notes"`
	ExpectedTotalCents *int64     `json:"expected_total_cents"` // the total the channel showed
	// ExpiresAt: the channel's deadline (DURABILITY-PLAN.md) — arriving later,
	// the order is refused (expired) and never charged. Optional.
	ExpiresAt *time.Time `json:"expires_at"`

	// TakenBy is the operator signed in to an operator channel (set by the
	// handler from the sign-in, never from the body).
	TakenBy *channelDom.Operator `json:"-"`
	// Account is the signed-in customer (X-Customer-Session; set by the
	// handler, never from the body).
	Account *customer.Customer `json:"-"`
}

// Accounts reads and keeps signed-in customers' saved cards (the customers service).
type Accounts interface {
	Card(ctx context.Context, c customer.Customer, id uuid.UUID) (customer.Card, error)
	RememberCard(ctx context.Context, customerID uuid.UUID, processorCustomerID string, card customer.Card) error
}

// SetAccounts plugs in customer accounts (saved cards).
func (s *Service) SetAccounts(a Accounts) { s.accounts = a }

var (
	clientIDRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	emailRe    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

// normPhone keeps the digits of a US phone number ("" when it isn't 10).
func normPhone(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if len(d) == 11 && d[0] == '1' {
		d = d[1:]
	}
	if len(d) != 10 {
		return ""
	}
	return d
}

func (r *Request) validate() *Error {
	bad := func(msg string) *Error { return refuse(http.StatusBadRequest, "bad_request", msg) }
	if !clientIDRe.MatchString(r.ClientOrderID) {
		return bad("client_order_id: 1–64 letters, digits or _ . : -")
	}
	if r.Type != "pickup" && r.Type != "delivery" {
		return bad("type must be pickup or delivery")
	}
	if len(r.Lines) == 0 || len(r.Lines) > 100 {
		return bad("an order has 1–100 lines")
	}
	r.Customer.Name = strings.TrimSpace(r.Customer.Name)
	if r.Customer.Name == "" || len([]rune(r.Customer.Name)) > 80 {
		return bad("customer.name is required (≤ 80)")
	}
	if r.Customer.Phone = normPhone(r.Customer.Phone); r.Customer.Phone == "" {
		return bad("customer.phone must be a 10-digit US number")
	}
	if r.Customer.Email = strings.TrimSpace(r.Customer.Email); r.Customer.Email != "" && !emailRe.MatchString(r.Customer.Email) {
		return bad("customer.email isn't an email address")
	}
	if r.Type == "delivery" {
		if r.Address == nil || strings.TrimSpace(r.Address.Line1) == "" || (strings.TrimSpace(r.Address.Zip) == "" && strings.TrimSpace(r.Address.City) == "") {
			return bad("a delivery needs address.line1 and a zip or city")
		}
	}
	if len([]rune(r.Notes)) > 500 {
		return bad("notes are limited to 500 characters")
	}
	if (r.Payment.CardID != "" || r.Payment.SaveCard) && (r.Payment.Method != payment.MethodCard || r.Account == nil) {
		return bad("payment.card_id / save_card need a card payment by a signed-in customer")
	}
	if r.Payment.CardID != "" {
		if _, err := uuid.Parse(r.Payment.CardID); err != nil {
			return bad("payment.card_id isn't a saved card")
		}
	}
	if r.Payment.Method != payment.MethodCash && r.Payment.Method != payment.MethodCard {
		return bad("payment.method must be cash or card")
	}
	return nil
}

// sourceEventID is the order's dedupe key: the channel (8 hex) + its key.
func sourceEventID(ch channelDom.Channel, clientOrderID string) string {
	return strings.ReplaceAll(ch.ID.String(), "-", "")[:8] + "-" + clientOrderID
}

// Place places a channel's order: the order (created = false when the same
// client_order_id was placed before — the same order comes back), *Pending
// (main is still at work: ask by key), or an *Error to show. With placements
// on (DURABILITY-PLAN.md) the work runs detached from ctx: a caller hanging
// up never cancels it.
func (s *Service) Place(ctx context.Context, ch channelDom.Channel, req Request) (*View, bool, error) {
	if !ch.Permissions.Orders {
		return nil, false, refuse(http.StatusForbidden, "forbidden", "this channel may not place orders")
	}
	if e := req.validate(); e != nil {
		return nil, false, e
	}
	if !containsStr(ch.Permissions.Payments, req.Payment.Method) {
		return nil, false, refuse(http.StatusForbidden, "payment_not_allowed", "this channel can't take "+req.Payment.Method+" orders")
	}
	if s.pl.placements != nil {
		return s.placeDurable(ctx, ch, req)
	}
	// The same key again: the same order.
	if v, err := s.byClient(ctx, ch, req.ClientOrderID); err == nil {
		return v, false, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	out := s.work(context.WithoutCancel(ctx), ch, req, repositories.Placement{}, false)
	return out.view, out.created, out.err
}

// work is one placement's work: check, price, hold the card, save. Every
// outcome is written to the placement (refused / parked / placed, voided).
// resume: the sweeper took over after the card call was made — the checks
// already passed then; it re-issues the same card call (Square returns the same
// payment) and saves the order only if the total is unchanged.
func (s *Service) work(ctx context.Context, ch channelDom.Channel, req Request, pl repositories.Placement, resume bool) outcome {
	key := req.ClientOrderID
	refused := func(e *Error) outcome {
		s.finish(ctx, ch, key, repositories.PlacementRefused, e)
		s.countRefusal(ch, e.Code)
		return outcome{err: e}
	}
	// parked: not final — the caller may come back with the same key.
	parked := func(waiting string, e *Error, err error) outcome {
		s.park(ctx, ch, key, waiting, e)
		if err == nil {
			err = e
		}
		return outcome{err: err}
	}
	tryAgain := refuse(http.StatusServiceUnavailable, "try_again", "The order couldn't be placed — try again.")

	gw, ok := s.gateways[req.Payment.Method]
	if !ok {
		return refused(refuse(http.StatusConflict, "payment_unavailable", "card payments aren't available yet — pay with cash"))
	}

	if !resume {
		// The store must take the order: now, or at a time it offers.
		st, err := s.status.Status(ctx, ch.TenantID, ch.LocationID)
		if err != nil {
			return parked("retry", tryAgain, err)
		}
		if req.PromisedAt == nil {
			switch {
			case st.State == "paused":
				msg := "The store isn't taking orders right now."
				if st.Pause != nil && st.Pause.Note != "" {
					msg = st.Pause.Note
				}
				return refused(refuse(http.StatusConflict, "store_paused", msg))
			case st.State == "closed":
				return refused(refuse(http.StatusConflict, "store_closed", "The store is closed — order for later."))
			}
		} else {
			loc, _ := time.LoadLocation(st.Store.Timezone)
			if loc == nil {
				loc = time.UTC
			}
			times, err := s.status.Times(ctx, ch.TenantID, ch.LocationID, req.Type, req.PromisedAt.In(loc).Format(time.DateOnly))
			if err != nil || !containsTime(times, *req.PromisedAt) {
				return refused(refuse(http.StatusConflict, "bad_time", "That time isn't available — pick another."))
			}
		}
		// The kitchen must be able to get it (a scheduled order far enough
		// ahead is taken; the owner is paged before it's due if the Pi is
		// still away).
		if s.pl.kitchen != nil && (req.PromisedAt == nil || req.PromisedAt.Sub(s.now()) < ScheduledLead) {
			off, since, err := s.pl.kitchen.Offline(ctx, ch.TenantID, ch.LocationID)
			if err != nil {
				s.logger.Warn().Err(err).Msg("kitchen check failed; taking the order")
			} else if off {
				return refused(s.kitchenOffline(ctx, ch, since))
			}
		}
	}

	// Price it again with the channel's prices and deals.
	pctx, _, err := s.pricing.Context(ctx, ch.TenantID, ch.LocationID, string(ch.Kind), ch.ViewID)
	if err != nil {
		return parked("retry", tryAgain, err)
	}
	cart := pricing.Cart{Type: req.Type, Payment: req.Payment.Method, Lines: req.Lines, Code: req.Code, TipCents: req.TipCents,
		TipPct: req.TipPct, FirstOrder: req.FirstOrder}
	q := pricing.Price(*pctx, cart, s.now())
	if resume && (len(q.Errors) > 0 || q.TotalCents != pl.ChargeCents) {
		// The menu moved since the card call: don't save a different order.
		if ok, _ := s.pl.placements.Finish(ctx, ch.ID, key, repositories.PlacementVoided, resultOf(errVoided)); ok {
			s.voidIfCharged(ctx, ch, pl)
			s.notNotify(ctx, ch, req, "its prices changed before main could finish it", true)
		}
		return outcome{err: errVoided}
	}
	if len(q.Errors) > 0 {
		return refused(&Error{Status: http.StatusUnprocessableEntity, Code: "cart_invalid", Message: q.Errors[0].Message, Issues: q.Errors})
	}
	if req.ExpectedTotalCents != nil && *req.ExpectedTotalCents != q.TotalCents {
		return refused(refuse(http.StatusConflict, "price_changed", "Prices changed — refresh and check your order."))
	}

	// A delivery address the store reaches (the website must; the call
	// center and counter may send it anyway — the tablet flags it).
	if req.Type == "delivery" && !resume {
		a := req.Address
		dc, err := s.status.DeliveryCheck(ctx, ch.TenantID, ch.LocationID, geo.Address{Line1: a.Line1, City: a.City, State: a.State, Zip: a.Zip})
		strict := ch.Kind == channelDom.KindWeb
		switch {
		case err != nil && strict:
			return parked("retry", refuse(http.StatusServiceUnavailable, "try_again", "We couldn't check the address — try again."), nil)
		case err != nil:
		case !strict:
		case dc.Result == "outside":
			return refused(refuse(http.StatusUnprocessableEntity, "outside_area", "We don't deliver to that address."))
		case dc.Result == "check" || dc.Result == "not_found":
			return refused(refuse(http.StatusUnprocessableEntity, "address_unknown", "We couldn't find that address — check it."))
		}
	}

	// Build the order before taking money, so nothing is charged for an
	// order that can't be built.
	// The customer's tracking link (an unguessable token, GET /track/v1/{token}).
	token := newTrackToken()
	if _, err := s.build(ctx, ch, req, pctx, q, nil, token); err != nil {
		var e *Error
		if errors.As(err, &e) {
			return refused(e)
		}
		return parked("retry", tryAgain, err)
	}
	charge, cerr := s.chargeFor(ctx, ch, req, q)
	if cerr != nil {
		return refused(cerr)
	}
	if req.Payment.Method == payment.MethodCard && s.pl.placements != nil {
		// Recorded first: whatever happens next, the sweeper knows a hold may exist.
		if err := s.pl.placements.StartCharge(ctx, ch.ID, key, q.TotalCents); err != nil {
			return parked("retry", tryAgain, err)
		}
		pl.ChargeStarted, pl.ChargeCents = true, q.TotalCents
	}
	res, err := gw.Charge(ctx, charge)
	switch {
	case errors.Is(err, payment.ErrDeclined):
		return refused(refuse(http.StatusPaymentRequired, "payment_declined", "The card was declined."))
	case errors.Is(err, payment.ErrVerify):
		return parked("verification", refuse(http.StatusPaymentRequired, "verification_required", "Your bank needs you to verify this card — try again."), nil)
	case errors.Is(err, payment.ErrBadToken):
		return refused(refuse(http.StatusPaymentRequired, "payment_declined", "The card couldn't be used."))
	case errors.Is(err, payment.ErrNotConnected):
		return refused(refuse(http.StatusConflict, "payment_unavailable", "Card payments aren't available right now — pay with cash."))
	case errors.Is(err, payment.ErrCanceled):
		return refused(refuse(http.StatusConflict, "payment_canceled", "The card payment for this order was canceled — place the order again."))
	case err != nil:
		return parked("retry", tryAgain, err)
	}
	o, err := s.build(ctx, ch, req, pctx, q, &res, token)
	if err != nil {
		s.refund(ctx, ch, gw, res, q.TotalCents)
		s.finish(ctx, ch, key, repositories.PlacementVoided, errVoided)
		return outcome{err: err}
	}
	if err := s.orders.Create(ctx, o); err != nil {
		switch {
		case errors.Is(err, order.ErrDuplicate): // the same key, placed at the same moment
			s.refund(ctx, ch, gw, res, q.TotalCents)
			v, verr := s.byClient(ctx, ch, req.ClientOrderID)
			if verr != nil {
				return outcome{err: err}
			}
			return outcome{view: v}
		case errors.Is(err, order.ErrPlacementGone): // finished elsewhere meanwhile
			s.refund(ctx, ch, gw, res, q.TotalCents)
			if s.pl.placements != nil {
				if p, gerr := s.pl.placements.Get(ctx, ch.ID, key); gerr == nil {
					return outcome{err: errorOf(p)}
				}
			}
			return outcome{err: errVoided}
		}
		s.refund(ctx, ch, gw, res, q.TotalCents)
		s.finish(ctx, ch, key, repositories.PlacementVoided, errVoided)
		return outcome{err: err}
	}
	if res.Saved != nil && req.Account != nil && s.accounts != nil {
		card := customer.Card{Gateway: res.Gateway, Ref: res.Saved.Ref, Brand: res.Saved.Brand, Last4: res.Saved.Last4,
			ExpMonth: res.Saved.ExpMonth, ExpYear: res.Saved.ExpYear}
		if err := s.accounts.RememberCard(context.WithoutCancel(ctx), req.Account.ID, res.CustomerRef, card); err != nil {
			s.logger.Warn().Err(err).Msg("saved card not remembered on the account (it stays on the processor)")
		}
	}
	s.logger.Info().Ctx(ctx).Str("order_id", uuid.UUID(o.ID()).String()).Str("channel", string(ch.Kind)).Msg("channel order placed")
	return outcome{view: viewOf(o, req.ClientOrderID, token), created: true}
}

// chargeFor is the card call for an order: always the same for the same
// request and total, so a re-issue with its idempotency key is the same call.
func (s *Service) chargeFor(ctx context.Context, ch channelDom.Channel, req Request, q pricing.Quote) (payment.Charge, *Error) {
	charge := payment.Charge{LocationID: ch.LocationID, AmountCents: q.TotalCents, TipCents: q.TipCents,
		Token: req.Payment.Token, VerificationToken: req.Payment.VerificationToken,
		IdempotencyKey: ch.ID.String() + ":" + req.ClientOrderID, ReferenceID: req.ClientOrderID,
		Note: ch.Name + " order", BuyerEmail: strings.TrimSpace(req.Customer.Email)}
	if a := req.Account; a != nil && req.Payment.Method == payment.MethodCard {
		charge.CustomerRef = a.SquareCustomerID
		charge.Buyer = payment.Buyer{Name: req.Customer.Name, Phone: req.Customer.Phone, Email: req.Customer.Email}
		if req.Payment.CardID != "" {
			card, err := s.savedCard(ctx, *a, req.Payment.CardID)
			if err != nil {
				return charge, refuse(http.StatusUnprocessableEntity, "card_not_found", "That saved card isn't on your account any more — pick another.")
			}
			charge.CardRef, charge.Token = card.Ref, ""
		} else {
			charge.SaveCard = req.Payment.SaveCard
		}
	}
	return charge, nil
}

// newTrackToken is 24 random bytes, URL-safe base64 (32 characters).
func newTrackToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on Linux
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// SetNotifier sends problems (a failed refund) to the owner.
func (s *Service) SetNotifier(n notify.Notifier) { s.notifier = n }

func (s *Service) refund(ctx context.Context, ch channelDom.Channel, gw payment.Gateway, res payment.Result, amount int64) {
	if res.Ref == "" {
		return
	}
	ctx = context.WithoutCancel(ctx)
	if err := gw.Refund(ctx, ch.LocationID, res.Ref, amount); err != nil {
		s.logger.Error().Ctx(ctx).Err(err).Str("gateway", res.Gateway).Msg("REFUND FAILED after a failed save — refund it by hand")
		if s.notifier != nil {
			_ = s.notifier.Notify(ctx, notify.Notice{LocationID: ch.LocationID, Kind: notify.KindRefundFailed, Title: "Refund failed",
				Message: fmt.Sprintf("A %s card charge of $%d.%02d couldn't be refunded after the order failed to save (ref %s). Refund it by hand.",
					ch.Name, amount/100, amount%100, res.Ref)})
		}
	}
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func containsTime(ts []time.Time, t time.Time) bool {
	for _, x := range ts {
		if x.Equal(t) {
			return true
		}
	}
	return false
}

// build makes the domain order from the priced cart (res = the payment, nil
// before charging).
func (s *Service) build(ctx context.Context, ch channelDom.Channel, req Request, pctx *pricing.Context, q pricing.Quote, res *payment.Result, token string) (*order.Order, error) {
	bank, err := s.bank.Bank(ctx, ch.LocationID)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, m := range bank.Menus {
		for _, c := range m.Categories {
			for _, it := range c.Items {
				names[it.ID.String()] = it.Name
			}
		}
	}
	type built struct {
		item  order.LineItem
		route string // the bank item whose printer it takes
	}
	var lines []built
	var comboOff []order.Discount
	var comboFee []order.Fee
	lineOf := func(r pricing.Resolved, qty int, note string, pos int) built {
		name := r.Name
		if n, ok := names[r.BankItem]; ok && r.BankItem != "" {
			name = n // the kitchen reads the bank's names
		}
		li := order.LineItem{ID: uuid.Must(uuid.NewV7()), Position: pos, Name: name, Size: r.SizeName, Quantity: qty,
			UnitPrice: order.Money(r.BaseCents), Notes: note}
		total := r.BaseCents * int64(qty)
		for k, o := range r.Options {
			// The name reads on its own ("Pepperoni (1st half)", "NO Onion")
			// for Pis that don't read portion / mode.
			child := order.LineItem{ID: uuid.Must(uuid.NewV7()), ParentID: &li.ID, Position: k, Quantity: qty,
				Name: o.Label, Portion: o.Portion, Mode: o.Mode, UnitPrice: order.Money(o.PriceCents)}
			if o.RemovedDefault {
				child.Quantity, child.UnitPrice = 1, 0
			}
			if o.Included {
				child.UnitPrice = 0
			}
			child.LineTotal = child.UnitPrice * order.Money(child.Quantity)
			total += int64(child.LineTotal)
			li.Children = append(li.Children, child)
		}
		li.LineTotal = order.Money(total)
		rate := r.BankItem
		if rate == "" {
			rate = r.RateItem
		}
		return built{item: li, route: rate}
	}
	for i, l := range req.Lines {
		qty := min(max(l.Qty, 1), 50)
		if l.DealID == "" {
			lines = append(lines, lineOf(pricing.Resolve(*pctx, l.Selection, 0), qty, strings.TrimSpace(l.Note), len(lines)))
			continue
		}
		var deal *pricing.Deal
		for k := range pctx.Deals {
			if pctx.Deals[k].ID == l.DealID {
				deal = &pctx.Deals[k]
			}
		}
		if deal == nil {
			return nil, refuse(http.StatusUnprocessableEntity, "cart_invalid", "A deal in your order has ended.")
		}
		var picksTotal int64
		for k, slot := range deal.Slots {
			if k >= len(l.Picks) {
				break
			}
			b := lineOf(pricing.Resolve(*pctx, l.Picks[k], slot.IncludedOptions), qty, "Combo: "+deal.Name, len(lines))
			picksTotal += int64(b.item.LineTotal)
			lines = append(lines, b)
		}
		// The combo's price against its items: the difference is a discount
		// (or, if the combo costs more, a fee) so the money still adds up.
		switch diff := picksTotal - q.Lines[i].TotalCents; {
		case diff > 0:
			comboOff = append(comboOff, order.Discount{Type: "deal", Label: "Combo: " + deal.Name, Amount: order.Money(diff)})
		case diff < 0:
			comboFee = append(comboFee, order.Fee{Type: "other", Label: "Combo: " + deal.Name, Amount: order.Money(-diff)})
		}
	}

	// Kitchen routing straight from the bank items.
	ids := make([]uuid.UUID, len(lines))
	for i, b := range lines {
		ids[i], _ = uuid.Parse(b.route)
	}
	routes, err := s.router.RouteBankItems(ctx, ch.LocationID, ids)
	if err != nil {
		return nil, err
	}
	items := make([]order.LineItem, len(lines))
	var subtotal order.Money
	for i, b := range lines {
		it := b.item
		it.MenuItemID, it.PrinterID, it.Routing = routes[i].MenuItemID, routes[i].PrinterID, routes[i].Routing
		items[i] = it
		subtotal += it.LineTotal
	}

	money := order.MoneyBreakdown{Subtotal: subtotal, Tax: order.Money(q.TaxCents), Tip: order.Money(q.TipCents),
		Total: order.Money(q.TotalCents), Fees: comboFee, Discounts: comboOff}
	for _, d := range q.Discounts {
		t := d.Type
		code := ""
		if d.Type == "code" {
			t, code = "promo", strings.TrimPrefix(d.Label, "Code ")
		}
		money.Discounts = append(money.Discounts, order.Discount{Type: t, Code: code, Label: d.Label, Amount: order.Money(d.AmountCents)})
	}
	if q.DeliveryFeeCents > 0 {
		_, driver := pricing.DeliverySplit(pctx.Settings, q.DeliveryFeeCents)
		money.Fees = append(money.Fees, order.Fee{Type: "delivery", Label: "Delivery fee", Amount: order.Money(q.DeliveryFeeCents),
			DriverCents: order.Money(driver)})
	}
	if q.ServiceFeeCents > 0 {
		money.Fees = append(money.Fees, order.Fee{Type: "service", Label: "Online fee", Amount: order.Money(q.ServiceFeeCents)})
	}
	if q.CardFeeCents > 0 {
		money.Fees = append(money.Fees, order.Fee{Type: "card", Label: "Card fee", Amount: order.Money(q.CardFeeCents)})
	}

	ful := order.Fulfillment{Type: order.FulfillmentType(req.Type), PromisedAt: req.PromisedAt}
	if req.Address != nil && req.Type == "delivery" {
		a := req.Address
		ful.Address = &order.Address{Line1: strings.TrimSpace(a.Line1), Line2: strings.TrimSpace(a.Line2), City: strings.TrimSpace(a.City),
			State: strings.TrimSpace(a.State), PostalCode: strings.TrimSpace(a.Zip), Country: "US", Notes: strings.TrimSpace(a.Notes)}
	}
	var pays []order.Payment
	payRef := []byte("{}")
	if res != nil {
		p := order.Payment{ID: uuid.Must(uuid.NewV7()), Processor: req.Payment.Method, Amount: order.Money(q.TotalCents)}
		switch res.Status {
		case payment.StatusCaptured:
			now := s.now().UTC()
			p.Status, p.Last4, p.AuthCode, p.CapturedAt = order.PaymentCaptured, res.Last4, res.AuthCode, &now
		case payment.StatusAuthorized: // a hold: captured at clock-out / Close batch / HoldFor
			p.Status, p.Last4, p.AuthCode = order.PaymentAuthorized, res.Last4, res.AuthCode
		default:
			p.Status = order.PaymentPending // cash: collected at the store / by the driver
		}
		pays = []order.Payment{p}
		payRef, _ = json.Marshal(map[string]string{"method": req.Payment.Method, "gateway": res.Gateway, "ref": res.Ref})
	}
	// The channel's request is the raw payload (as the vendor's JSON is for
	// Brygid's orders) — without the card token.
	rawReq := req
	rawReq.Payment.Token, rawReq.Payment.VerificationToken = "", ""
	raw, err := json.Marshal(rawReq)
	if err != nil {
		return nil, err
	}
	meta := map[string]any{"channel": map[string]any{"id": ch.ID.String(), "kind": string(ch.Kind), "name": ch.Name},
		"client_order_id": req.ClientOrderID}
	ref := &order.ChannelRef{ID: ch.ID, ClientOrderID: req.ClientOrderID, Payment: payRef, TrackToken: token,
		Receipt: ch.Kind == channelDom.KindWeb && strings.TrimSpace(req.Customer.Email) != ""}
	if req.Account != nil {
		id := req.Account.ID
		ref.CustomerID = &id
	}
	if s.pl.placements != nil {
		ref.Placement, ref.PlacementDueCheck = true, dueCheck(req.PromisedAt)
	}
	if res != nil && res.Status == payment.StatusAuthorized {
		ref.Hold = &order.CardHold{Gateway: res.Gateway, Ref: res.Ref, AmountCents: q.TotalCents - q.TipCents, TipCents: q.TipCents,
			DueAt: s.now().Add(HoldFor)}
	}
	if op := req.TakenBy; op != nil {
		meta["operator"] = map[string]any{"id": op.ID.String(), "display_name": op.DisplayName}
		id := op.ID
		ref.OperatorID = &id
	}
	return order.NewOrder(order.NewOrderInput{
		RawPayload:     raw,
		Channel:        ref,
		TenantID:       order.TenantID(ch.TenantID),
		LocationID:     order.LocationID(ch.LocationID),
		Source:         order.Source(ch.Kind),
		SourceEventID:  sourceEventID(ch, req.ClientOrderID),
		Customer:       order.Customer{DisplayName: req.Customer.Name, Phone: req.Customer.Phone, Email: req.Customer.Email},
		Fulfillment:    ful,
		Items:          items,
		Money:          money,
		Currency:       "USD",
		Payments:       pays,
		PlacedAt:       s.now().UTC(),
		Notes:          strings.TrimSpace(req.Notes),
		SourceMetadata: meta,
	})
}

// --- reading orders back ---------------------------------------------------------------

// View is an order as its channel sees it.
type View struct {
	ID               string     `json:"id"`
	ClientOrderID    string     `json:"client_order_id"`
	TrackToken       string     `json:"track_token"` // the customer's tracking link: GET /track/v1/{track_token}
	Status           string     `json:"status"`      // received | scheduled | accepted | ready | on_delivery | completed | cancelled
	Type             string     `json:"type"`
	PlacedAt         time.Time  `json:"placed_at"`
	PromisedAt       *time.Time `json:"promised_at"`
	KitchenNumber    *int       `json:"kitchen_number"`
	EstimatedReadyAt *time.Time `json:"estimated_ready_at"`
	Customer         struct {
		Name string `json:"name"`
	} `json:"customer"`
	TakenBy *TakenBy   `json:"taken_by"` // the operator who took it (operator channels)
	Lines   []ViewLine `json:"lines"`
	Money   ViewMoney  `json:"money"`
	Pay     struct {
		Method string `json:"method"` // cash | card
		Status string `json:"status"` // due | paid
	} `json:"payment"`
}

type TakenBy struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type ViewLine struct {
	Name       string   `json:"name"`
	Size       string   `json:"size"`
	Qty        int      `json:"qty"`
	TotalCents int64    `json:"total_cents"`
	Mods       []string `json:"mods"`
	Notes      string   `json:"notes"`
}

type ViewMoney struct {
	SubtotalCents int64        `json:"subtotal_cents"`
	Discounts     []ViewAmount `json:"discounts"`
	Fees          []ViewAmount `json:"fees"`
	TaxCents      int64        `json:"tax_cents"`
	TipCents      int64        `json:"tip_cents"`
	TotalCents    int64        `json:"total_cents"`
}

type ViewAmount struct {
	Label       string `json:"label"`
	AmountCents int64  `json:"amount_cents"`
}

func viewOf(o *order.Order, clientOrderID, trackToken string) *View {
	snap := o.Snapshot()
	v := &View{ID: snap.ID, ClientOrderID: clientOrderID, TrackToken: trackToken, Status: o.Status().Public(), Type: string(o.Fulfillment().Type),
		PlacedAt: o.PlacedAt(), PromisedAt: o.Fulfillment().PromisedAt, KitchenNumber: snap.KitchenNumber, EstimatedReadyAt: snap.EstimatedReadyAt,
		Lines: []ViewLine{}}
	v.Customer.Name = o.Customer().DisplayName
	if op, ok := snap.SourceMeta["operator"].(map[string]any); ok {
		id, _ := op["id"].(string)
		name, _ := op["display_name"].(string)
		v.TakenBy = &TakenBy{ID: id, DisplayName: name}
	}
	for _, it := range o.Items() {
		l := ViewLine{Name: it.Name, Size: it.Size, Qty: it.Quantity, TotalCents: int64(it.LineTotal), Mods: []string{}, Notes: it.Notes}
		for _, c := range it.Children {
			l.Mods = append(l.Mods, c.Name)
		}
		v.Lines = append(v.Lines, l)
	}
	m := o.Money()
	v.Money = ViewMoney{SubtotalCents: int64(m.Subtotal), TaxCents: int64(m.Tax), TipCents: int64(m.Tip), TotalCents: int64(m.Total),
		Discounts: []ViewAmount{}, Fees: []ViewAmount{}}
	for _, d := range m.Discounts {
		v.Money.Discounts = append(v.Money.Discounts, ViewAmount{Label: d.Label, AmountCents: int64(d.Amount)})
	}
	for _, f := range m.Fees {
		v.Money.Fees = append(v.Money.Fees, ViewAmount{Label: f.Label, AmountCents: int64(f.Amount)})
	}
	v.Pay.Method, v.Pay.Status = payment.MethodCash, "due"
	for _, p := range o.Payments() {
		v.Pay.Method = p.Processor
		if p.Status == order.PaymentCaptured || p.Status == order.PaymentAuthorized {
			v.Pay.Status = "paid"
		}
	}
	return v
}

func (s *Service) byClient(ctx context.Context, ch channelDom.Channel, clientOrderID string) (*View, error) {
	link, err := s.links.ByClient(ctx, ch.ID, clientOrderID)
	if err != nil {
		return nil, err
	}
	o, err := s.orders.GetByID(ctx, order.Scope{TenantID: order.TenantID(ch.TenantID), LocationID: order.LocationID(ch.LocationID)}, order.ID(link.OrderID))
	if err != nil {
		return nil, err
	}
	return viewOf(o, clientOrderID, link.TrackToken), nil
}

// ErrNotFound: not one of this channel's orders.
var ErrNotFound = errors.New("order not found")

// Get is one of the channel's own orders.
func (s *Service) Get(ctx context.Context, ch channelDom.Channel, orderID uuid.UUID) (*View, error) {
	link, err := s.links.Get(ctx, orderID)
	if err != nil || link.ChannelID == nil || *link.ChannelID != ch.ID {
		return nil, ErrNotFound
	}
	o, err := s.orders.GetByID(ctx, order.Scope{TenantID: order.TenantID(ch.TenantID), LocationID: order.LocationID(ch.LocationID)}, order.ID(orderID))
	if err != nil {
		return nil, ErrNotFound
	}
	return viewOf(o, link.ClientOrderID, link.TrackToken), nil
}

// List is the channel's own orders, newest first (before = a page's cursor).
func (s *Service) List(ctx context.Context, ch channelDom.Channel, before time.Time, limit int) ([]*View, error) {
	links, err := s.links.List(ctx, ch.ID, before, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*View, 0, len(links))
	scope := order.Scope{TenantID: order.TenantID(ch.TenantID), LocationID: order.LocationID(ch.LocationID)}
	for _, l := range links {
		o, err := s.orders.GetByID(ctx, scope, order.ID(l.OrderID))
		if err != nil {
			continue // deleted meanwhile
		}
		out = append(out, viewOf(o, l.ClientOrderID, l.TrackToken))
	}
	return out, nil
}

func (s *Service) savedCard(ctx context.Context, a customer.Customer, id string) (customer.Card, error) {
	cid, err := uuid.Parse(id)
	if err != nil || s.accounts == nil {
		return customer.Card{}, customer.ErrNotFound
	}
	return s.accounts.Card(ctx, a, cid)
}

// --- a customer's orders (weborder stage 2) ---------------------------------------------

// ForLinks turns an account's order links into views (gone orders skipped).
func (s *Service) ForLinks(ctx context.Context, ch channelDom.Channel, links []repositories.ChannelOrder) []*View {
	out := make([]*View, 0, len(links))
	scope := order.Scope{TenantID: order.TenantID(ch.TenantID), LocationID: order.LocationID(ch.LocationID)}
	for _, l := range links {
		o, err := s.orders.GetByID(ctx, scope, order.ID(l.OrderID))
		if err != nil {
			continue
		}
		out = append(out, viewOf(o, l.ClientOrderID, l.TrackToken))
	}
	return out
}

// ReorderResult is a past order's lines for a new cart: what is still on
// the channel's menu, priced today (the quote), and what isn't any more.
type ReorderResult struct {
	Lines   []pricing.Line  `json:"lines"`
	Dropped []string        `json:"dropped"` // names no longer orderable
	Quote   *pricing.Quote  `json:"quote"`
	Issues  []pricing.Issue `json:"issues"`
}

// Reorder builds a cart from one of the account's past orders.
func (s *Service) Reorder(ctx context.Context, ch channelDom.Channel, link repositories.ChannelOrder, typ string) (*ReorderResult, error) {
	o, err := s.orders.GetByID(ctx, order.Scope{TenantID: order.TenantID(ch.TenantID), LocationID: order.LocationID(ch.LocationID)}, order.ID(link.OrderID))
	if err != nil {
		return nil, ErrNotFound
	}
	var past Request
	if err := json.Unmarshal(o.RawPayload(), &past); err != nil || len(past.Lines) == 0 {
		return nil, ErrNotFound
	}
	if typ != "pickup" && typ != "delivery" {
		typ = past.Type
	}
	pctx, _, err := s.pricing.Context(ctx, ch.TenantID, ch.LocationID, string(ch.Kind), ch.ViewID)
	if err != nil {
		return nil, err
	}
	out := &ReorderResult{Lines: []pricing.Line{}, Dropped: []string{}, Issues: []pricing.Issue{}}
	// Price each line alone: a line the menu can't take any more is dropped.
	names := map[int]string{} // the kitchen lines match the cart lines one to one unless a deal split them
	if items := o.Items(); len(items) == len(past.Lines) {
		for i, it := range items {
			names[i] = it.Name
		}
	}
	for i, l := range past.Lines {
		q := pricing.Price(*pctx, pricing.Cart{Type: typ, Payment: payment.MethodCard, Lines: []pricing.Line{l}}, s.now())
		if len(q.Errors) > 0 {
			name := names[i]
			if name == "" {
				name = fmt.Sprintf("item %d", i+1)
			}
			out.Dropped = append(out.Dropped, name)
			continue
		}
		out.Lines = append(out.Lines, l)
	}
	if len(out.Lines) > 0 {
		q := pricing.Price(*pctx, pricing.Cart{Type: typ, Payment: payment.MethodCard, Lines: out.Lines}, s.now())
		out.Quote, out.Issues = &q, q.Errors
	}
	return out, nil
}
