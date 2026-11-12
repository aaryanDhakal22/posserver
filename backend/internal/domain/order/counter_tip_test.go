package order

import (
	"testing"

	"github.com/google/uuid"
)

// A tip added to a card payment afterwards (agent contract 2.10p) reaches
// main as a payment with a zero amount and the tip; a zero amount with no
// tip is stored and flagged (M-2: never refused), a negative tip refused.
func TestCounterPaymentZeroAmountTip(t *testing.T) {
	p := CounterPayment{ID: uuid.NewString(), Method: "manual_card", AmountCents: 0, TipCents: 300}
	cp, err := p.Check()
	if err != nil || cp.Amount != 0 || cp.Tip != 300 || cp.Status != PaymentCaptured {
		t.Fatalf("tip payment: %+v %v", cp, err)
	}
	p.TipCents = 0
	if cp, err := p.Check(); err != nil || len(cp.Flags) != 1 || cp.Flags[0].Code != FlagZeroAmount {
		t.Fatalf("zero amount, no tip: %+v %v", cp.Flags, err)
	}
	p.AmountCents = MaxPaymentCents + 1
	if cp, err := p.Check(); err != nil || len(cp.Flags) != 1 || cp.Flags[0].Code != FlagAmountOutOfRange {
		t.Fatalf("out of range: %+v %v", cp.Flags, err)
	}
	p.AmountCents, p.TipCents = 100, -1
	if _, err := p.Check(); err == nil {
		t.Fatal("a negative tip can't be stored: refused")
	}
}
