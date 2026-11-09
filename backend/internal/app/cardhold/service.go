// Package cardhold captures online card holds (weborder stage 2). A web
// card order is authorized at checkout; main captures it with its final tip
//
//   - when the order's driver clocks out (their deliveries of that shift),
//   - at Close batch (everything else in the batch),
//   - or by itself 24 h after the order (orderport.HoldFor) when neither came,
//
// and voids it when the order is cancelled (refunds it if it was captured).
// A tip added after the order (Quibase's ADD TIP → floor event order.tip) is
// put on the hold when Square lets the hold grow, else charged separately on
// the card saved for that order. Failures retry with backoff and page the
// owner; nothing here blocks the floor.
package cardhold

import (
	"context"
	"errors"
	"fmt"
	"time"

	"quiccpos/main/internal/domain/notify"
	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/domain/payment"
	"quiccpos/main/internal/infra/database/repositories"
	"quiccpos/main/internal/infra/square"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// Repo persists holds (repositories.SquareRepository).
type Repo interface {
	Hold(ctx context.Context, orderID uuid.UUID) (repositories.CardHold, error)
	HoldByRef(ctx context.Context, locationID uuid.UUID, ref string) (repositories.CardHold, error)
	Holds(ctx context.Context, locationID uuid.UUID, limit int) ([]repositories.CardHold, error)
	Claim(ctx context.Context, n int) ([]repositories.CardHold, error)
	RequestCapture(ctx context.Context, locationID uuid.UUID, orderIDs []uuid.UUID, reason string) (int64, error)
	RequestCaptureForBatch(ctx context.Context, locationID uuid.UUID, batchID int64) (int64, error)
	RequestCaptureForDriver(ctx context.Context, locationID uuid.UUID, driverID string, from, to time.Time) (int64, error)
	SetAddedTip(ctx context.Context, locationID, orderID uuid.UUID, cents int64) (bool, error)
	Captured(ctx context.Context, orderID uuid.UUID, cents int64, tipRef, tipNote string) error
	Cancelled(ctx context.Context, orderID uuid.UUID, note string) error
	Retry(ctx context.Context, orderID uuid.UUID, next time.Time, lastError string) error
	Failed(ctx context.Context, orderID uuid.UUID, lastError string) error
	SetDue(ctx context.Context, orderID uuid.UUID, due time.Time) error
	HoldRows(ctx context.Context, locationID uuid.UUID, status string, limit int) ([]repositories.HoldRow, error)
}

// Orders reads an order (the buyer behind a saved card).
type Orders interface {
	GetByID(ctx context.Context, scope order.Scope, id order.ID) (*order.Order, error)
}

type Service struct {
	repo     Repo
	holders  map[string]payment.Holder // gateway name → adapter
	orders   Orders
	notifier notify.Notifier
	logger   zerolog.Logger
	now      func() time.Time
	kick     chan struct{} // wakes the worker (a clock-out / Close batch asked for captures)
	final    func(ctx context.Context, orderID uuid.UUID)
}

// OnAmountChanged is told when a capture charged more than the hold showed
// (a late tip), so the customer gets a final receipt.
func (s *Service) OnAmountChanged(f func(ctx context.Context, orderID uuid.UUID)) { s.final = f }

func NewService(repo Repo, holders map[string]payment.Holder, orders Orders, lg zerolog.Logger) *Service {
	return &Service{repo: repo, holders: holders, orders: orders, logger: lg.With().Str("module", "cardhold").Logger(), now: time.Now,
		kick: make(chan struct{}, 1)}
}

// wake runs the worker now instead of at its next tick.
func (s *Service) wake() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// SetNotifier pages the owner on payment problems.
func (s *Service) SetNotifier(n notify.Notifier) { s.notifier = n }

// MaxAttempts: a capture that keeps failing stops retrying (failed) and the
// owner settles it in Square.
const MaxAttempts = 8

func backoff(attempt int) time.Duration {
	steps := []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour}
	if attempt < len(steps) {
		return steps[attempt]
	}
	return 4 * time.Hour
}

func money(c int64) string { return fmt.Sprintf("$%d.%02d", c/100, c%100) }

func (s *Service) page(ctx context.Context, locationID uuid.UUID, title, msg string) {
	s.logger.Warn().Str("location_id", locationID.String()).Msg(title + ": " + msg)
	if s.notifier != nil {
		_ = s.notifier.Notify(context.WithoutCancel(ctx), notify.Notice{LocationID: locationID, Kind: notify.KindPaymentProblem, Title: title, Message: msg})
	}
}

func holdOf(h repositories.CardHold) payment.Hold {
	return payment.Hold{OrderID: h.OrderID, LocationID: h.LocationID, Ref: h.PaymentRef, AmountCents: h.AmountCents, TipCents: h.TipCents}
}

// --- triggers ---------------------------------------------------------------------------

// DriverClockedOut asks to capture the card deliveries the driver took out
// between clock-in and clock-out.
func (s *Service) DriverClockedOut(ctx context.Context, locationID uuid.UUID, driverID string, clockIn, clockOut time.Time) {
	n, err := s.repo.RequestCaptureForDriver(ctx, locationID, driverID, clockIn.Add(-time.Minute), clockOut.Add(time.Minute))
	if err != nil {
		s.logger.Error().Err(err).Msg("capture on clock-out")
	} else if n > 0 {
		s.logger.Info().Int64("holds", n).Str("driver_id", driverID).Msg("card holds to capture (clock-out)")
		s.wake()
	}
}

// BatchClosed asks to capture every hold of the batch.
func (s *Service) BatchClosed(ctx context.Context, locationID uuid.UUID, batchID int64) {
	n, err := s.repo.RequestCaptureForBatch(ctx, locationID, batchID)
	if err != nil {
		s.logger.Error().Err(err).Msg("capture on close batch")
	} else if n > 0 {
		s.logger.Info().Int64("holds", n).Int64("batch_id", batchID).Msg("card holds to capture (close batch)")
		s.wake()
	}
}

// SetTip sets the tip added after the order (absolute: the whole added tip;
// false = the order has no open card hold — cash, or captured already).
func (s *Service) SetTip(ctx context.Context, locationID, orderID uuid.UUID, cents int64) (bool, error) {
	if cents < 0 {
		return false, fmt.Errorf("tip can't be negative")
	}
	return s.repo.SetAddedTip(ctx, locationID, orderID, cents)
}

// OrderCancelled voids the order's hold, or refunds it if it was captured.
func (s *Service) OrderCancelled(ctx context.Context, locationID, orderID uuid.UUID) {
	ctx = context.WithoutCancel(ctx)
	h, err := s.repo.Hold(ctx, orderID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && h.LocationID != locationID) {
		return
	}
	if err != nil {
		s.logger.Error().Err(err).Msg("cancel: read hold")
		return
	}
	gw, ok := s.holders[h.Gateway]
	if !ok {
		return
	}
	switch h.Status {
	case "held":
		if err := gw.Void(ctx, holdOf(h)); err != nil {
			s.page(ctx, locationID, "Card hold not released", fmt.Sprintf("An order was cancelled but its %s card hold couldn't be released (%v). Void payment %s in Square.",
				money(h.AmountCents+h.TipCents), err, h.PaymentRef))
			return
		}
		_ = s.repo.Cancelled(ctx, orderID, "order cancelled")
	case "captured":
		if err := gw.Refund(ctx, locationID, h.PaymentRef, h.CapturedCents-tipCharged(h)); err != nil {
			s.page(ctx, locationID, "Refund failed", fmt.Sprintf("A cancelled order's %s card payment couldn't be refunded (%v). Refund payment %s in Square.",
				money(h.CapturedCents), err, h.PaymentRef))
			return
		}
		if h.TipPaymentRef != "" {
			if err := gw.Refund(ctx, locationID, h.TipPaymentRef, h.AddedTipCents); err != nil {
				s.page(ctx, locationID, "Refund failed", fmt.Sprintf("A cancelled order's separate %s tip couldn't be refunded (%v). Refund payment %s in Square.",
					money(h.AddedTipCents), err, h.TipPaymentRef))
			}
		}
		_ = s.repo.Cancelled(ctx, orderID, "order cancelled after capture: refunded")
	}
}

// tipCharged is the part of CapturedCents that went on the separate tip payment.
func tipCharged(h repositories.CardHold) int64 {
	if h.TipPaymentRef != "" {
		return h.AddedTipCents
	}
	return 0
}

// SquarePayment reconciles a Square webhook: a hold voided / expired at
// Square is marked cancelled and the owner told.
func (s *Service) SquarePayment(ctx context.Context, locationID uuid.UUID, p *square.Payment) {
	if p == nil || (p.Status != square.StatusCanceled && p.Status != square.StatusFailed) {
		return
	}
	h, err := s.repo.HoldByRef(ctx, locationID, p.ID)
	if err != nil || h.Status != "held" {
		return
	}
	_ = s.repo.Cancelled(ctx, h.OrderID, "voided at Square")
	s.page(ctx, locationID, "Card hold voided", fmt.Sprintf("A %s card hold was voided in Square before main captured it (payment %s). The order is unpaid.",
		money(h.AmountCents+h.TipCents), p.ID))
}

// --- the worker ---------------------------------------------------------------------------

// Run captures due holds every 15 s until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
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

// Tick captures what is due now; it returns how many holds it worked on.
func (s *Service) Tick(ctx context.Context) int {
	n := 0
	for {
		hs, err := s.repo.Claim(ctx, 20)
		if err != nil {
			s.logger.Error().Err(err).Msg("claim card holds")
			return n
		}
		for _, h := range hs {
			s.capture(ctx, h)
		}
		n += len(hs)
		if len(hs) < 20 {
			return n
		}
	}
}

// CaptureNow captures one order's hold at once (dev tests, the SPA later).
func (s *Service) CaptureNow(ctx context.Context, locationID, orderID uuid.UUID) (repositories.CardHold, error) {
	if _, err := s.repo.RequestCapture(ctx, locationID, []uuid.UUID{orderID}, "manual"); err != nil {
		return repositories.CardHold{}, err
	}
	s.Tick(ctx)
	return s.repo.Hold(ctx, orderID)
}

// MakeDue moves a hold's safety capture to now (dev tests).
func (s *Service) MakeDue(ctx context.Context, orderID uuid.UUID) error {
	return s.repo.SetDue(ctx, orderID, s.now().Add(-time.Second))
}

// Hold is an order's hold.
func (s *Service) Hold(ctx context.Context, orderID uuid.UUID) (repositories.CardHold, error) {
	return s.repo.Hold(ctx, orderID)
}

// Holds lists a store's recent holds.
func (s *Service) Holds(ctx context.Context, locationID uuid.UUID, limit int) ([]repositories.CardHold, error) {
	return s.repo.Holds(ctx, locationID, limit)
}

func (s *Service) retry(ctx context.Context, h repositories.CardHold, err error) {
	if h.Attempts+1 >= MaxAttempts || payment.IsPermanent(err) {
		_ = s.repo.Failed(ctx, h.OrderID, err.Error())
		s.page(ctx, h.LocationID, "Card capture failed", fmt.Sprintf("main couldn't capture a %s card hold (payment %s): %v. Capture it in Square before it expires (7 days).",
			money(h.AmountCents+h.TipCents+h.AddedTipCents), h.PaymentRef, err))
		return
	}
	_ = s.repo.Retry(ctx, h.OrderID, s.now().Add(backoff(h.Attempts)), err.Error())
	s.logger.Warn().Err(err).Str("order_id", h.OrderID.String()).Int("attempt", h.Attempts+1).Msg("card capture will retry")
}

func (s *Service) capture(ctx context.Context, h repositories.CardHold) {
	gw, ok := s.holders[h.Gateway]
	if !ok {
		_ = s.repo.Failed(ctx, h.OrderID, "no adapter for "+h.Gateway)
		return
	}
	hold := holdOf(h)
	separateTip := int64(0)
	tipNote := ""
	if h.AddedTipCents > 0 {
		err := gw.RaiseTip(ctx, hold, h.TipCents+h.AddedTipCents)
		switch {
		case err == nil:
			hold.TipCents += h.AddedTipCents
			tipNote = "added tip on the card hold"
		case errors.Is(err, payment.ErrCantRaise):
			separateTip = h.AddedTipCents
		case errors.Is(err, payment.ErrHoldGone):
			s.gone(ctx, h)
			return
		case payment.IsPermanent(err):
			separateTip = h.AddedTipCents // e.g. completed by an earlier try: charge the tip on its own
		default:
			s.retry(ctx, h, err)
			return
		}
	}
	cents, err := gw.Complete(ctx, hold)
	if errors.Is(err, payment.ErrHoldGone) {
		s.gone(ctx, h)
		return
	}
	if err != nil {
		s.retry(ctx, h, err)
		return
	}
	tipRef := ""
	if separateTip > 0 {
		buyer := payment.Buyer{}
		if s.orders != nil {
			if o, err := s.orders.GetByID(ctx, order.Scope{TenantID: order.TenantID(h.TenantID), LocationID: order.LocationID(h.LocationID)},
				order.ID(h.OrderID)); err == nil {
				c := o.Customer()
				buyer = payment.Buyer{Name: c.DisplayName, Phone: c.Phone, Email: c.Email}
			}
		}
		ref, terr := gw.ChargeTip(ctx, hold, separateTip, buyer)
		if terr != nil && !payment.IsPermanent(terr) {
			// The hold is captured; retry only the tip (the keys keep it to one charge).
			ref, terr = gw.ChargeTip(ctx, hold, separateTip, buyer)
		}
		if terr != nil {
			tipNote = "added tip " + money(separateTip) + " NOT charged: " + terr.Error()
			s.page(ctx, h.LocationID, "Tip not charged", fmt.Sprintf("A %s tip added after the order couldn't be charged on the card (%v). The order's %s was captured (payment %s). Settle the tip with the driver.",
				money(separateTip), terr, money(cents), h.PaymentRef))
		} else {
			tipRef, tipNote = ref, "added tip charged separately"
			cents += separateTip
		}
	}
	if err := s.repo.Captured(ctx, h.OrderID, cents, tipRef, tipNote); err != nil {
		s.logger.Error().Err(err).Str("order_id", h.OrderID.String()).Msg("card hold captured but not saved (the next try sees it captured)")
		return
	}
	s.logger.Info().Str("order_id", h.OrderID.String()).Int64("cents", cents).Str("reason", reasonOr(h.CaptureReason, "safety")).Msg("card hold captured")
	if s.final != nil && cents != h.AmountCents+h.TipCents {
		s.final(ctx, h.OrderID)
	}
}

func reasonOr(r, d string) string {
	if r == "" {
		return d
	}
	return r
}

func (s *Service) gone(ctx context.Context, h repositories.CardHold) {
	_ = s.repo.Cancelled(ctx, h.OrderID, "voided or expired at the processor")
	s.page(ctx, h.LocationID, "Card hold gone", fmt.Sprintf("A %s card hold was voided or expired before main could capture it (payment %s). The order is unpaid.",
		money(h.AmountCents+h.TipCents), h.PaymentRef))
}

// Rows lists a store's holds with their orders (the Payments page; "" = any status).
func (s *Service) Rows(ctx context.Context, locationID uuid.UUID, status string, limit int) ([]repositories.HoldRow, error) {
	return s.repo.HoldRows(ctx, locationID, status, limit)
}

// ErrNotHeld: the hold isn't waiting any more (charged, released or failed).
var ErrNotHeld = errors.New("this card order isn't waiting to be charged")

// ChargeNow captures one of the store's waiting holds at once (the
// manager's "Charge now").
func (s *Service) ChargeNow(ctx context.Context, locationID, orderID uuid.UUID) (repositories.CardHold, error) {
	h, err := s.repo.Hold(ctx, orderID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && h.LocationID != locationID) {
		return h, pgx.ErrNoRows
	}
	if err != nil {
		return h, err
	}
	if h.Status != "held" {
		return h, ErrNotHeld
	}
	return s.CaptureNow(ctx, locationID, orderID)
}
