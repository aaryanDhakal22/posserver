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
	"quiccpos/main/internal/observability"

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
	Captured(ctx context.Context, h repositories.CardHold, onHold, tip, separate int64, tipRef, tipNote string, seenTip int64) (bool, error)
	Cancelled(ctx context.Context, orderID uuid.UUID, note string) error
	Refunded(ctx context.Context, h repositories.CardHold, cents int64, ref string, tipCents int64, tipRef, note string) error
	ClaimUndo(ctx context.Context, n int) ([]repositories.CardHold, error)
	UndoFailed(ctx context.Context, orderID uuid.UUID, lastError string) error
	TipCap(ctx context.Context, locationID uuid.UUID) (int64, error)
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
	meters   *observability.Meters
}

// SetMeters records captures, voids, refunds and failures (L-3).
func (s *Service) SetMeters(m *observability.Meters) { s.meters = m }

// ErrTipTooBig: an added tip over the store's cap (M-20).
var ErrTipTooBig = errors.New("the tip is over the store's tip cap")

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
	s.logger.Warn().Ctx(ctx).Str("location_id", locationID.String()).Msg(title + ": " + msg)
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
	limit, err := s.repo.TipCap(ctx, locationID)
	if err != nil {
		return false, err
	}
	if cents > limit {
		s.logger.Warn().Ctx(ctx).Str("order_id", orderID.String()).Int64("tip_cents", cents).Int64("cap_cents", limit).
			Msg("added tip over the store's tip cap: refused")
		return false, ErrTipTooBig
	}
	return s.repo.SetAddedTip(ctx, locationID, orderID, cents)
}

// OrderCancelled releases a cancelled order's hold now (voids it while held,
// refunds it once captured). The order's own transaction already asked for
// it (the card_holds_on_order_cancel trigger, M-3), so nothing captures it
// any more and the worker keeps retrying a void / refund that fails; this
// only does the work at once instead of at the worker's next tick.
func (s *Service) OrderCancelled(ctx context.Context, locationID, orderID uuid.UUID) {
	s.TickUndo(context.WithoutCancel(ctx))
}

// TickUndo voids / refunds the holds of cancelled orders that are due; it
// returns how many it worked on.
func (s *Service) TickUndo(ctx context.Context) int {
	n := 0
	for {
		hs, err := s.repo.ClaimUndo(ctx, 20)
		if err != nil {
			s.logger.Error().Ctx(ctx).Err(err).Msg("claim card holds to release")
			return n
		}
		for _, h := range hs {
			s.undo(ctx, h)
		}
		n += len(hs)
		if len(hs) < 20 {
			return n
		}
	}
}

func (s *Service) undoRetry(ctx context.Context, h repositories.CardHold, what string, err error) {
	s.count(ctx, h.LocationID, "failed", what, 0)
	if h.Attempts+1 >= MaxAttempts || payment.IsPermanent(err) {
		if ferr := s.repo.UndoFailed(ctx, h.OrderID, err.Error()); ferr != nil {
			s.logger.Error().Ctx(ctx).Err(ferr).Str("order_id", h.OrderID.String()).Msg("card hold: could not record the failed release")
		}
		title, act := "Card hold not released", "Void payment "+h.PaymentRef+" in Square."
		if what == "refund" {
			title, act = "Refund failed", "Refund payment "+h.PaymentRef+" in Square."
		}
		s.page(ctx, h.LocationID, title, fmt.Sprintf("A cancelled order's %s card payment couldn't be released (%v). %s",
			money(h.AmountCents+h.TipCents+h.AddedTipCents), err, act))
		return
	}
	if rerr := s.repo.Retry(ctx, h.OrderID, s.now().Add(backoff(h.Attempts)), err.Error()); rerr != nil {
		s.logger.Error().Ctx(ctx).Err(rerr).Str("order_id", h.OrderID.String()).Msg("card hold: could not schedule the retry")
	}
	s.logger.Warn().Ctx(ctx).Err(err).Str("order_id", h.OrderID.String()).Str("payment_ref", h.PaymentRef).Str("op", what).
		Int("attempt", h.Attempts+1).Msg("card hold release will retry")
}

func (s *Service) undo(ctx context.Context, h repositories.CardHold) {
	gw, ok := s.holders[h.Gateway]
	if !ok {
		if err := s.repo.UndoFailed(ctx, h.OrderID, "no adapter for "+h.Gateway); err != nil {
			s.logger.Error().Ctx(ctx).Err(err).Msg("card hold: could not record the failed release")
		}
		return
	}
	switch h.Status {
	case "held":
		if err := gw.Void(ctx, holdOf(h)); err != nil && !errors.Is(err, payment.ErrHoldGone) {
			s.undoRetry(ctx, h, "void", err)
			return
		}
		if err := s.repo.Cancelled(ctx, h.OrderID, "order cancelled"); err != nil {
			s.logger.Error().Ctx(ctx).Err(err).Str("order_id", h.OrderID.String()).Msg("card hold voided at Square but not saved (the next try voids again: harmless)")
			return
		}
		s.count(ctx, h.LocationID, "voided", "void", h.AmountCents+h.TipCents)
		s.logger.Info().Ctx(ctx).Str("order_id", h.OrderID.String()).Str("payment_ref", h.PaymentRef).
			Int64("cents", h.AmountCents+h.TipCents).Str("reason", "order cancelled").Msg("card hold voided")
	case "captured":
		main := h.CapturedCents - tipCharged(h) - h.RefundedCents
		if main > 0 {
			if err := gw.Refund(ctx, h.LocationID, h.PaymentRef, main); err != nil {
				s.undoRetry(ctx, h, "refund", err)
				return
			}
		}
		tip := int64(0)
		if h.TipPaymentRef != "" && h.AddedTipCents > 0 {
			if err := gw.Refund(ctx, h.LocationID, h.TipPaymentRef, h.AddedTipCents); err != nil {
				// The main refund went through (its idempotency key keeps a
				// retry to one refund); only the tip is retried.
				s.undoRetry(ctx, h, "refund", err)
				return
			}
			tip = h.AddedTipCents
		}
		if err := s.repo.Refunded(ctx, h, main, h.PaymentRef, tip, h.TipPaymentRef, "order cancelled after capture: refunded"); err != nil {
			s.logger.Error().Ctx(ctx).Err(err).Str("order_id", h.OrderID.String()).Msg("card refund made at Square but not saved (the next try refunds again with the same key)")
			return
		}
		s.count(ctx, h.LocationID, "refunded", "refund", main+tip)
		s.logger.Info().Ctx(ctx).Str("order_id", h.OrderID.String()).Str("payment_ref", h.PaymentRef).
			Int64("cents", main+tip).Str("reason", "order cancelled after capture").Msg("card hold refunded")
	}
}

// count records a hold outcome (L-3): payments.card_holds{result, op} and,
// for money that moved, payments.captured_cents / payments.refunded_cents.
func (s *Service) count(ctx context.Context, locationID uuid.UUID, result, op string, cents int64) {
	if s.meters == nil {
		return
	}
	s.meters.RecordCardHold(ctx, locationID, result, op, cents)
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
	s.must(ctx, h, "cancelled", s.repo.Cancelled(ctx, h.OrderID, "voided at Square"))
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
		s.TickUndo(ctx)
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
		s.must(ctx, h, "failed", s.repo.Failed(ctx, h.OrderID, err.Error()))
		s.count(ctx, h.LocationID, "failed", "capture", 0)
		s.page(ctx, h.LocationID, "Card capture failed", fmt.Sprintf("main couldn't capture a %s card hold (payment %s): %v. Capture it in Square before it expires (7 days).",
			money(h.AmountCents+h.TipCents+h.AddedTipCents), h.PaymentRef, err))
		return
	}
	s.must(ctx, h, "retry", s.repo.Retry(ctx, h.OrderID, s.now().Add(backoff(h.Attempts)), err.Error()))
	s.logger.Warn().Err(err).Str("order_id", h.OrderID.String()).Int("attempt", h.Attempts+1).Msg("card capture will retry")
}

func (s *Service) capture(ctx context.Context, h repositories.CardHold) {
	gw, ok := s.holders[h.Gateway]
	if !ok {
		s.must(ctx, h, "failed", s.repo.Failed(ctx, h.OrderID, "no adapter for "+h.Gateway))
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
	onHold := cents
	if tipRef != "" {
		onHold -= separateTip
	}
	saved, err := s.repo.Captured(ctx, h, onHold, hold.TipCents, cents-onHold, tipRef, tipNote, h.AddedTipCents)
	if err != nil {
		s.logger.Error().Ctx(ctx).Err(err).Str("order_id", h.OrderID.String()).Msg("card hold captured but not saved (the next try sees it captured)")
		s.count(ctx, h.LocationID, "failed", "save", 0)
		s.page(ctx, h.LocationID, "Card capture not saved", fmt.Sprintf("A %s card payment (%s) was captured but main couldn't record it (%v). It is retried.",
			money(cents), h.PaymentRef, err))
		return
	}
	if !saved {
		// A tip was added (or the order cancelled) while capturing: look again.
		s.logger.Warn().Ctx(ctx).Str("order_id", h.OrderID.String()).Msg("card hold changed while capturing: looked at again")
		if err := s.repo.Retry(ctx, h.OrderID, s.now(), "changed while capturing"); err != nil {
			s.logger.Error().Ctx(ctx).Err(err).Msg("card hold: could not requeue")
		}
		s.wake()
		return
	}
	s.count(ctx, h.LocationID, "captured", "capture", cents)
	s.logger.Info().Ctx(ctx).Str("order_id", h.OrderID.String()).Str("payment_ref", h.PaymentRef).Int64("cents", cents).
		Str("reason", reasonOr(h.CaptureReason, "safety")).Msg("card hold captured")
	if s.final != nil && cents != h.AmountCents+h.TipCents {
		s.final(ctx, h.OrderID)
	}
}

// must logs and pages a hold write that failed (L-3: they were discarded).
func (s *Service) must(ctx context.Context, h repositories.CardHold, what string, err error) {
	if err == nil {
		return
	}
	s.logger.Error().Ctx(ctx).Err(err).Str("order_id", h.OrderID.String()).Str("payment_ref", h.PaymentRef).Str("write", what).
		Msg("card hold write failed")
	s.page(ctx, h.LocationID, "Card hold not saved", fmt.Sprintf("main couldn't record a card hold change (%s, payment %s): %v. Check it on the Payments page.",
		what, h.PaymentRef, err))
}

func reasonOr(r, d string) string {
	if r == "" {
		return d
	}
	return r
}

func (s *Service) gone(ctx context.Context, h repositories.CardHold) {
	s.must(ctx, h, "cancelled", s.repo.Cancelled(ctx, h.OrderID, "voided or expired at the processor"))
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
