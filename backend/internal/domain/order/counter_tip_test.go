package order

import (
	"testing"

	"github.com/google/uuid"
)

// A tip added to a card payment afterwards (agent contract 2.10p) reaches
// main as a payment with a zero amount and the tip; a zero amount with no
// tip is still refused.
func TestCounterPaymentZeroAmountTip(t *testing.T) {
	p := CounterPayment{ID: uuid.NewString(), Method: "manual_card", AmountCents: 0, TipCents: 300}
	cp, err := p.Check()
	if err != nil || cp.Amount != 0 || cp.Tip != 300 || cp.Status != PaymentCaptured {
		t.Fatalf("tip payment: %+v %v", cp, err)
	}
	p.TipCents = 0
	if _, err := p.Check(); err == nil {
		t.Fatal("zero amount, no tip: accepted")
	}
}
