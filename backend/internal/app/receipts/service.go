// Package receipts emails customers their web order receipts (weborder
// stage 2): one when the order is placed (with the tracking link) and a
// final one only when the charged amount changed (a tip added later) or the
// order was cancelled. Receipts are queued in the order's transaction
// (email_receipts) and sent by a worker, retried with backoff. From
// "<Store name>" <MAIL_FROM_ADDRESS>, Reply-To the store's email when set.
package receipts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/infra/database/repositories"
	"quiccpos/main/internal/infra/mail"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// Repo is the receipts queue (repositories.ReceiptRepository).
type Repo interface {
	QueueFinal(ctx context.Context, orderID uuid.UUID) (bool, error)
	Claim(ctx context.Context, n int) ([]repositories.Receipt, error)
	Done(ctx context.Context, orderID uuid.UUID, kind, status, messageID, lastError string) error
	Retry(ctx context.Context, orderID uuid.UUID, kind string, next time.Time, lastError string) error
	ForOrder(ctx context.Context, orderID uuid.UUID) ([]repositories.Receipt, error)
}

type Orders interface {
	GetByID(ctx context.Context, scope order.Scope, id order.ID) (*order.Order, error)
}

type Locations interface {
	GetLocation(ctx context.Context, id uuid.UUID) (*tenancy.Location, error)
}

type Links interface {
	Get(ctx context.Context, orderID uuid.UUID) (repositories.ChannelOrder, error)
}

// Store is what the store's website knows: its primary host and reply email.
type Store interface {
	PrimaryHost(ctx context.Context, locationID uuid.UUID) string
	ReplyEmail(ctx context.Context, locationID uuid.UUID) (string, error)
}

type Holds interface {
	Hold(ctx context.Context, orderID uuid.UUID) (repositories.CardHold, error)
}

// TrackPath is where the storefront shows an order's tracking page.
const TrackPath = "/track/"

type Service struct {
	repo   Repo
	orders Orders
	locs   Locations
	links  Links
	store  Store
	holds  Holds
	sender mail.Sender // nil = mail is off: receipts are skipped
	from   string
	logger zerolog.Logger
	kick   chan struct{}
	now    func() time.Time
}

func NewService(repo Repo, orders Orders, locs Locations, links Links, store Store, holds Holds, sender mail.Sender, from string,
	lg zerolog.Logger) *Service {
	return &Service{repo: repo, orders: orders, locs: locs, links: links, store: store, holds: holds, sender: sender, from: from,
		logger: lg.With().Str("module", "receipts").Logger(), kick: make(chan struct{}, 1), now: time.Now}
}

// Wake sends what is queued now instead of at the next tick.
func (s *Service) Wake() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// QueueFinal queues the final receipt of an order that got one when placed.
func (s *Service) QueueFinal(ctx context.Context, orderID uuid.UUID) {
	if ok, err := s.repo.QueueFinal(context.WithoutCancel(ctx), orderID); err != nil {
		s.logger.Error().Err(err).Str("order_id", orderID.String()).Msg("final receipt not queued")
	} else if ok {
		s.Wake()
	}
}

// ForOrder lists an order's receipts (admin / tests).
func (s *Service) ForOrder(ctx context.Context, orderID uuid.UUID) ([]repositories.Receipt, error) {
	return s.repo.ForOrder(ctx, orderID)
}

// Run sends queued receipts every 10 s (or when woken) until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

// MaxAttempts: a receipt that keeps failing is given up (failed).
const MaxAttempts = 6

func backoff(n int) time.Duration {
	steps := []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute, 2 * time.Hour}
	if n < len(steps) {
		return steps[n]
	}
	return 6 * time.Hour
}

// Tick sends what is due; it returns how many receipts it worked on.
func (s *Service) Tick(ctx context.Context) int {
	rs, err := s.repo.Claim(ctx, 20)
	if err != nil {
		s.logger.Error().Err(err).Msg("claim receipts")
		return 0
	}
	for _, r := range rs {
		s.send(ctx, r)
	}
	return len(rs)
}

var errNoEmail = errors.New("no email on the order")

func (s *Service) send(ctx context.Context, r repositories.Receipt) {
	msg, err := s.build(ctx, r)
	switch {
	case errors.Is(err, errNoEmail):
		_ = s.repo.Done(ctx, r.OrderID, r.Kind, "skipped", "", err.Error())
		return
	case errors.Is(err, pgx.ErrNoRows):
		_ = s.repo.Done(ctx, r.OrderID, r.Kind, "skipped", "", "order gone")
		return
	case err != nil:
		s.retry(ctx, r, err)
		return
	}
	if s.sender == nil {
		_ = s.repo.Done(ctx, r.OrderID, r.Kind, "skipped", "", "mail is off")
		return
	}
	id, err := s.sender.Send(ctx, msg)
	if err != nil {
		var se *mail.SendError
		if errors.As(err, &se) && se.Permanent {
			_ = s.repo.Done(ctx, r.OrderID, r.Kind, "failed", "", err.Error())
			s.logger.Warn().Err(err).Str("order_id", r.OrderID.String()).Msg("receipt refused")
			return
		}
		s.retry(ctx, r, err)
		return
	}
	_ = s.repo.Done(ctx, r.OrderID, r.Kind, "sent", id, "")
	s.logger.Info().Str("order_id", r.OrderID.String()).Str("kind", r.Kind).Msg("receipt sent")
}

func (s *Service) retry(ctx context.Context, r repositories.Receipt, err error) {
	if r.Attempts+1 >= MaxAttempts {
		_ = s.repo.Done(ctx, r.OrderID, r.Kind, "failed", "", err.Error())
		s.logger.Warn().Err(err).Str("order_id", r.OrderID.String()).Msg("receipt given up")
		return
	}
	_ = s.repo.Retry(ctx, r.OrderID, r.Kind, s.now().Add(backoff(r.Attempts)), err.Error())
}

func (s *Service) build(ctx context.Context, r repositories.Receipt) (mail.Message, error) {
	loc, err := s.locs.GetLocation(ctx, r.LocationID)
	if err != nil {
		return mail.Message{}, pgx.ErrNoRows
	}
	o, err := s.orders.GetByID(ctx, order.Scope{TenantID: order.TenantID(loc.TenantID), LocationID: order.LocationID(loc.ID)}, order.ID(r.OrderID))
	if err != nil {
		return mail.Message{}, pgx.ErrNoRows
	}
	to := strings.TrimSpace(o.Customer().Email)
	if to == "" {
		return mail.Message{}, errNoEmail
	}
	d := s.data(ctx, r, loc, o)
	reply, _ := s.store.ReplyEmail(ctx, loc.ID)
	return mail.Message{FromName: loc.Name, FromAddress: s.from, ReplyTo: reply, To: to, Subject: Subject(d), Text: Text(d), HTML: HTML(d)}, nil
}

func (s *Service) data(ctx context.Context, r repositories.Receipt, loc *tenancy.Location, o *order.Order) Data {
	tz, _ := time.LoadLocation(loc.Timezone)
	if tz == nil {
		tz = time.UTC
	}
	id := uuid.UUID(o.ID()).String()
	d := Data{Kind: r.Kind, StoreName: loc.Name, StorePhone: phone(loc.Phone), StoreAddr: strings.TrimSpace(loc.AddressLine1 + " " + loc.AddressLine2),
		OrderRef: "#" + strings.ToUpper(id[len(id)-6:]), PlacedAt: o.PlacedAt().In(tz), Type: string(o.Fulfillment().Type),
		Customer: o.Customer().DisplayName, Cancelled: o.Status() == order.StatusCancelled, Notes: o.Notes()}
	if p := o.Fulfillment().PromisedAt; p != nil {
		t := p.In(tz)
		d.PromisedAt = &t
	}
	if a := o.Fulfillment().Address; a != nil && d.Type == "delivery" {
		d.DeliverTo = strings.Join(nonEmpty(a.Line1, a.Line2, strings.TrimSpace(a.City+", "+a.State+" "+a.PostalCode)), ", ")
	}
	for _, it := range o.Items() {
		l := Line{Name: it.Name, Size: it.Size, Qty: it.Quantity, TotalCents: int64(it.LineTotal), Notes: it.Notes}
		for _, c := range it.Children {
			l.Mods = append(l.Mods, c.Name)
		}
		d.Lines = append(d.Lines, l)
	}
	m := o.Money()
	d.Subtotal, d.Tax, d.Tip, d.Total = int64(m.Subtotal), int64(m.Tax), int64(m.Tip), int64(m.Total)
	for _, x := range m.Discounts {
		d.Discounts = append(d.Discounts, Amount{Label: x.Label, Cents: int64(x.Amount)})
	}
	for _, x := range m.Fees {
		d.Fees = append(d.Fees, Amount{Label: x.Label, Cents: int64(x.Amount)})
	}
	if l, err := s.links.Get(ctx, uuid.UUID(o.ID())); err == nil && l.TrackToken != "" {
		if host := s.store.PrimaryHost(ctx, loc.ID); host != "" {
			d.TrackURL = "https://" + host + TrackPath + l.TrackToken
		}
	}
	d.PaymentNote = s.paymentNote(ctx, o, &d)
	return d
}

func nonEmpty(xs ...string) []string {
	var out []string
	for _, x := range xs {
		if x = strings.Trim(strings.TrimSpace(x), ","); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func phone(p string) string {
	if len(p) == 10 {
		return "(" + p[:3] + ") " + p[3:6] + "-" + p[6:]
	}
	return p
}

// paymentNote says how the order is / was paid, and fixes the totals on a
// final receipt to what was charged.
func (s *Service) paymentNote(ctx context.Context, o *order.Order, d *Data) string {
	var card *order.Payment
	for _, p := range o.Payments() {
		if p.Processor == "card" {
			p := p
			card = &p
		}
	}
	if card == nil {
		switch {
		case d.Cancelled:
			return "Nothing was charged."
		case d.Type == "delivery":
			return "Pay the driver in cash when your order arrives."
		}
		return "Pay at the store when you pick it up."
	}
	ending := ""
	if card.Last4 != "" {
		ending = " •••• " + card.Last4
	}
	h, err := s.holds.Hold(ctx, uuid.UUID(o.ID()))
	if err != nil { // no hold: charged at checkout (the test card)
		if d.Cancelled {
			return "Your card" + ending + " payment was refunded. It can take 5–10 business days to show."
		}
		return "Paid by card" + ending + "."
	}
	switch h.Status {
	case "cancelled":
		if strings.Contains(h.LastError, "refunded") {
			return "Your card" + ending + " payment was refunded. It can take 5–10 business days to show."
		}
		return "The hold on your card" + ending + " was released — you weren't charged."
	case "captured":
		d.Total = h.CapturedCents
		added := h.AddedTipCents
		if strings.Contains(h.TipNote, "NOT charged") {
			added = 0
		}
		d.Tip = h.TipCents + added
		note := "Charged to your card" + ending + ": " + money(h.CapturedCents)
		if added > 0 {
			note += fmt.Sprintf(" (including a %s tip added after your order)", money(added))
		}
		return note + "."
	}
	return "Your card" + ending + " is authorized for " + money(h.AmountCents+h.TipCents) + ". It's charged when your order is done."
}

// StoreFuncs adapts two functions to Store.
type StoreFuncs struct {
	Host  func(ctx context.Context, locationID uuid.UUID) string
	Reply func(ctx context.Context, locationID uuid.UUID) (string, error)
}

func (f StoreFuncs) PrimaryHost(ctx context.Context, locationID uuid.UUID) string {
	return f.Host(ctx, locationID)
}
func (f StoreFuncs) ReplyEmail(ctx context.Context, locationID uuid.UUID) (string, error) {
	return f.Reply(ctx, locationID)
}
