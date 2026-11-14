package cardhold

import (
	"context"
	"fmt"
	"time"

	"quiccpos/main/internal/domain/payment"
	"quiccpos/main/internal/infra/database/repositories"

	"github.com/google/uuid"
)

// Counter delivery holds (agent contract §2.10ao, the owner 2026-11-14): a
// delivery's card taken at the counter (the call center, Centurion's Manual
// Entry or reader) is authorized only, so the driver's typed tip goes on it.
// The Pi marks such a payment held in its order.payment; main records the
// hold here (gateway payment.GatewaySquareCounter) and this same machine
// captures it with the typed tip at the driver's clock-out, at Close batch,
// or 24 h after (HoldFor). Differences from a website hold:
//
//   - its tip is the Pi's (square.tip_adjust → CounterTip), never charged on a
//     saved card when Square won't raise the hold (the owner adds it in Square);
//   - once captured, money back is the Pi's (it refunds through square.refund),
//     so an order cancel never refunds a captured counter hold here;
//   - a waiting hold whose money came back at the counter (a tender void: the
//     Pi's square.refund cancelled it at Square) is marked cancelled.

// HoldFor is how long a counter hold waits for a clock-out / Close batch
// before it is captured by itself (the website's 24 h).
var HoldFor = 24 * time.Hour

// CounterRepo is the card hold store's counter side (repositories.SquareRepository).
type CounterRepo interface {
	InsertCounterHold(ctx context.Context, h repositories.CounterHoldIn) (repositories.CounterHoldOut, error)
	CounterHoldTip(ctx context.Context, locationID uuid.UUID, ref string, whole int64) (bool, error)
	CounterHoldReleased(ctx context.Context, locationID uuid.UUID, ref, note string) (bool, error)
	CounterHoldRefunded(ctx context.Context, locationID, orderID, refundOf uuid.UUID, cents int64, note string) (bool, error)
	ClearUndo(ctx context.Context, orderID uuid.UUID, note string) error
}

// CounterPayment is a counter payment the Pi marked held.
type CounterPayment struct {
	TenantID, LocationID, OrderID uuid.UUID
	Ref                           string // Square's payment id
	AmountCents, TipCents         int64
}

func (s *Service) counterRepo() (CounterRepo, bool) {
	r, ok := s.repo.(CounterRepo)
	return r, ok
}

// CounterHold records a counter payment the Pi marked held (order.placed /
// order.payment, applied or a replay). A delivery's waits for the floor's
// triggers; another kind's is captured at once. A held payment main can't
// track (the order has another hold already) is captured at once too.
func (s *Service) CounterHold(ctx context.Context, p CounterPayment) {
	r, ok := s.counterRepo()
	if !ok || p.Ref == "" || p.AmountCents <= 0 {
		return
	}
	log := s.logger.With().Str("order_id", p.OrderID.String()).Str("payment_ref", p.Ref).Int64("amount_cents", p.AmountCents).Logger()
	out, err := r.InsertCounterHold(ctx, repositories.CounterHoldIn{OrderID: p.OrderID, TenantID: p.TenantID, LocationID: p.LocationID,
		PaymentRef: p.Ref, AmountCents: p.AmountCents, TipCents: p.TipCents, DueAt: s.now().Add(HoldFor)})
	if err != nil {
		log.Error().Ctx(ctx).Err(err).Msg("counter card hold not recorded")
		s.page(ctx, p.LocationID, "Card hold not saved", fmt.Sprintf("A %s counter card hold (payment %s) couldn't be recorded (%v). Square charges it by itself in 36 h; check it on the Payments page.",
			money(p.AmountCents+p.TipCents), p.Ref, err))
		return
	}
	switch {
	case out.OrderGone:
		log.Warn().Ctx(ctx).Msg("counter card hold: no such order")
	case out.Inserted:
		log.Info().Ctx(ctx).Bool("delivery", out.Delivery).Msg("counter card hold recorded")
		if !out.Delivery {
			s.wake() // not a delivery (any more): captured now
		}
	case !out.Tracked:
		// One hold per order: a second held card on it is captured now.
		log.Warn().Ctx(ctx).Msg("counter card hold: the order has another hold; captured at once")
		s.captureUntracked(ctx, p)
	}
}

// captureUntracked captures a held counter payment main keeps no row for.
func (s *Service) captureUntracked(ctx context.Context, p CounterPayment) {
	gw, ok := s.holders[payment.GatewaySquareCounter]
	if !ok {
		return
	}
	h := payment.Hold{OrderID: p.OrderID, LocationID: p.LocationID, Ref: p.Ref, AmountCents: p.AmountCents, TipCents: p.TipCents}
	if _, err := gw.Complete(ctx, h); err != nil {
		s.count(ctx, p.LocationID, "failed", "capture", 0)
		s.page(ctx, p.LocationID, "Card capture failed", fmt.Sprintf("main couldn't capture a %s counter card hold (payment %s): %v. Square charges it by itself in 36 h, or capture it in Square.",
			money(p.AmountCents+p.TipCents), p.Ref, err))
		return
	}
	s.count(ctx, p.LocationID, "captured", "capture", p.AmountCents+p.TipCents)
}

// CounterTip keeps the payment's whole tip on its waiting counter hold (the
// Pi's square.tip_adjust at a press of Close batch / a clock-out); the capture
// puts it on the card. false = no waiting counter hold for the payment.
func (s *Service) CounterTip(ctx context.Context, locationID uuid.UUID, ref string, whole int64) (bool, error) {
	r, ok := s.counterRepo()
	if !ok {
		return false, nil
	}
	return r.CounterHoldTip(ctx, locationID, ref, whole)
}

// CounterReleased marks a waiting counter hold cancelled: Square voided it
// (square.refund of the whole hold at the counter).
func (s *Service) CounterReleased(ctx context.Context, locationID uuid.UUID, ref, note string) {
	r, ok := s.counterRepo()
	if !ok {
		return
	}
	if done, err := r.CounterHoldReleased(ctx, locationID, ref, note); err != nil {
		s.logger.Error().Ctx(ctx).Err(err).Str("payment_ref", ref).Msg("counter card hold: the void not recorded (the worker finds it voided)")
	} else if done {
		s.count(ctx, locationID, "voided", "void", 0)
		s.logger.Info().Ctx(ctx).Str("payment_ref", ref).Str("reason", note).Msg("card hold voided")
	}
}

// CounterRefunded: a refund the Pi recorded on payment refundOf (cents back,
// tip included). A waiting counter hold it covers whole was cancelled at
// Square by that refund (main's square.refund): marked cancelled, so the
// worker never tries to capture it.
func (s *Service) CounterRefunded(ctx context.Context, locationID, orderID, refundOf uuid.UUID, cents int64) {
	r, ok := s.counterRepo()
	if !ok || cents <= 0 {
		return
	}
	if done, err := r.CounterHoldRefunded(ctx, locationID, orderID, refundOf, cents, "voided at the counter (the tender's money back)"); err != nil {
		s.logger.Error().Ctx(ctx).Err(err).Str("order_id", orderID.String()).Msg("counter card hold: the refund not recorded on it")
	} else if done {
		s.logger.Info().Ctx(ctx).Str("order_id", orderID.String()).Msg("counter card hold cancelled (its money came back at the counter)")
	}
}
