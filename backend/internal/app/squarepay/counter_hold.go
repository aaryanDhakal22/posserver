package squarepay

import (
	"context"
	"errors"

	"quiccpos/main/internal/domain/payment"

	"github.com/google/uuid"
)

// Counter delivery holds (agent contract §2.10ao, main contract §2.5e, the
// owner 2026-11-14): a delivery's card taken at the counter (the call center,
// Centurion's Manual Entry, Centurion's reader) is authorized only, so the
// driver's typed card tip goes on it before it is captured. main's card hold
// machine (app/cardhold) captures it like a website hold: at the driver's
// clock-out, at Close batch, or 24 h after; the Pi's tender void cancels it.
// Holds of this kind carry the gateway CounterGatewayName.

// CounterGatewayName labels a counter delivery's card hold (card_holds.gateway).
const CounterGatewayName = payment.GatewaySquareCounter

// DelayComplete: a counter hold nobody captured or voided by Square's delay
// (36 h for a card read in person, 7 days for a typed card) is charged by
// Square itself, never dropped (the money the cashier saw approved is
// collected even if main never captured it).
const DelayComplete = "COMPLETE"

// CounterHolds is the card hold machine's side of a counter hold
// (cardhold.Service), told by square.tip_adjust and square.refund.
type CounterHolds interface {
	// CounterTip records the payment's whole tip on its waiting hold, put on
	// the card when it is captured. false = no waiting counter hold for it.
	CounterTip(ctx context.Context, locationID uuid.UUID, paymentRef string, wholeTip int64) (bool, error)
	// CounterReleased marks a waiting counter hold cancelled (Square voided it).
	CounterReleased(ctx context.Context, locationID uuid.UUID, paymentRef, note string)
}

// SetCounterHolds plugs in the card hold machine (cmd/api/main.go).
func (s *Service) SetCounterHolds(h CounterHolds) { s.counterHolds = h }

// CounterGateway is the Square gateway for counter holds: the same hold calls,
// but a tip Square won't put on the hold is never charged on a saved card (the
// card was in the store's hands, not the customer's: the owner adds it in
// Square, as batch F's not_adjustable).
type CounterGateway struct{ *Gateway }

// CounterGateway is the counter holds' adapter (cmd/api/main.go registers it
// under CounterGatewayName).
func (s *Service) CounterGateway() *CounterGateway { return &CounterGateway{Gateway: s.Gateway()} }

var _ payment.Holder = (*CounterGateway)(nil)

// ErrCounterTipNotCharged: a counter hold's tip Square wouldn't take.
var ErrCounterTipNotCharged = errors.New("a counter card's tip goes on its own payment only: add it in Square")

// ChargeTip never charges a counter hold's tip separately.
func (g *CounterGateway) ChargeTip(context.Context, payment.Hold, int64, payment.Buyer) (string, error) {
	return "", payment.Permanent{Err: ErrCounterTipNotCharged}
}
