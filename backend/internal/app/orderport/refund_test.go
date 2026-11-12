package orderport

import (
	"context"
	"testing"

	channelDom "quiccpos/main/internal/domain/channel"
	"quiccpos/main/internal/domain/payment"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type refundGW struct{ refunds []string }

func (g *refundGW) Charge(context.Context, payment.Charge) (payment.Result, error) {
	return payment.Result{}, nil
}
func (g *refundGW) Refund(_ context.Context, _ uuid.UUID, ref string, _ int64) error {
	g.refunds = append(g.refunds, ref)
	return nil
}

type savedLinks struct {
	Links
	saved map[string]bool
}

func (l savedLinks) HoldRefSaved(_ context.Context, _, ref string) (bool, error) {
	return l.saved[ref], nil
}

// M-5: the racer that lost a duplicate got the saved order's own payment
// (same idempotency key): it is never refunded; a payment nobody holds is.
func TestRefundSkipsTheSavedOrdersPayment(t *testing.T) {
	gw := &refundGW{}
	s := &Service{links: savedLinks{saved: map[string]bool{"sq-saved": true}}, logger: zerolog.Nop()}
	ch := channelDom.Channel{ID: uuid.New(), LocationID: uuid.New()}
	s.refund(context.Background(), ch, gw, payment.Result{Gateway: "square", Ref: "sq-saved"}, 1234)
	if len(gw.refunds) != 0 {
		t.Fatalf("refunded the saved order's payment: %v", gw.refunds)
	}
	s.refund(context.Background(), ch, gw, payment.Result{Gateway: "square", Ref: "sq-orphan"}, 1234)
	if len(gw.refunds) != 1 || gw.refunds[0] != "sq-orphan" {
		t.Fatalf("an orphan charge must be refunded: %v", gw.refunds)
	}
}
