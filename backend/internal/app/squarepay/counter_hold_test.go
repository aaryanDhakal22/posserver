package squarepay

import (
	"context"
	"testing"

	"quiccpos/main/internal/infra/square"

	"github.com/google/uuid"
)

// Counter delivery holds (agent contract §2.10ao, the owner 2026-11-14).

type fakeCounterHolds struct {
	tipOK    bool
	tips     map[string]int64
	released []string
}

func (f *fakeCounterHolds) CounterTip(_ context.Context, _ uuid.UUID, ref string, whole int64) (bool, error) {
	if !f.tipOK {
		return false, nil
	}
	if f.tips == nil {
		f.tips = map[string]int64{}
	}
	f.tips[ref] = whole
	return true, nil
}

func (f *fakeCounterHolds) CounterReleased(_ context.Context, _ uuid.UUID, ref, _ string) {
	f.released = append(f.released, ref)
}

func keyedHold(nonce, key string, cents, tip int64, hold bool) map[string]any {
	b := keyed(nonce, key, cents, tip)
	b["hold"] = hold
	return b
}

// A delivery's typed card (hold: true, the Pi's decision) is authorized
// only: APPROVED on Square, held in the answer; a pickup's is completed.
func TestKeyedDeliveryCardIsAHoldAndAPickupsIsCompleted(t *testing.T) {
	s, sc, tok := testRefundRig(t)
	r, err := call(t, s.HandleKeyedPayment, sc, keyedHold("cnon:card-nonce-ok", "hold-k1", 2500, 0, true))
	if err != nil || !r.OK || r.Payment == nil {
		t.Fatalf("held keyed = %+v, %v", r, err)
	}
	if r.Payment.Status != square.StatusApproved || !r.Payment.Held {
		t.Fatalf("a delivery's card must be a hold: %+v", r.Payment)
	}
	got, err := s.client.GetPayment(context.Background(), tok, r.Payment.ID)
	if err != nil || got.Status != square.StatusApproved {
		t.Fatalf("on Square: %+v %v", got, err)
	}
	p, err := call(t, s.HandleKeyedPayment, sc, keyedHold("cnon:card-nonce-ok", "hold-k2", 2500, 0, false))
	if err != nil || !p.OK || p.Payment.Status != square.StatusCompleted || p.Payment.Held {
		t.Fatalf("a pickup's card must be completed: %+v, %v", p.Payment, err)
	}
}

// A tender void of a held card gives the money back by cancelling the hold
// (never a refund: there is nothing captured); part of it = captured first,
// then refunded.
func TestRefundOfAHeldCardCancelsIt(t *testing.T) {
	s, sc, tok := testRefundRig(t)
	hooks := &fakeCounterHolds{}
	s.SetCounterHolds(hooks)
	r, _ := call(t, s.HandleKeyedPayment, sc, keyedHold("cnon:card-nonce-ok", "void-k1", 2000, 300, true))
	ref, err := call(t, s.HandleRefund, sc, map[string]any{"payment_id": r.Payment.ID, "amount_cents": 2300, "idempotency_key": "tv-1"})
	if err != nil || !ref.OK || !ref.Voided || ref.Status != square.StatusCanceled || ref.RefundID != r.Payment.ID {
		t.Fatalf("void of a hold = %+v, %v", ref, err)
	}
	got, _ := s.client.GetPayment(context.Background(), tok, r.Payment.ID)
	if got.Status != square.StatusCanceled || (got.RefundedMoney != nil && got.RefundedMoney.Amount != 0) {
		t.Fatalf("on Square: %+v (want CANCELED, nothing refunded)", got)
	}
	if len(hooks.released) != 1 || hooks.released[0] != r.Payment.ID {
		t.Fatalf("hold machine told: %v", hooks.released)
	}
	// Part of a hold: captured, then refunded.
	r2, _ := call(t, s.HandleKeyedPayment, sc, keyedHold("cnon:card-nonce-ok", "void-k2", 2000, 0, true))
	part, err := call(t, s.HandleRefund, sc, map[string]any{"payment_id": r2.Payment.ID, "amount_cents": 500, "idempotency_key": "tv-2"})
	if err != nil || !part.OK || part.Voided || part.RefundID == "" {
		t.Fatalf("part of a hold = %+v, %v", part, err)
	}
	got2, _ := s.client.GetPayment(context.Background(), tok, r2.Payment.ID)
	if got2.Status != square.StatusCompleted || got2.RefundedMoney == nil || got2.RefundedMoney.Amount != 500 {
		t.Fatalf("on Square: %+v (want COMPLETED, 500 refunded)", got2)
	}
}

// square.tip_adjust on a waiting counter hold keeps the tip for the capture
// (the hold machine puts it on the card first): ok, Square not changed yet.
func TestTipAdjustOnACounterHoldIsKeptForTheCapture(t *testing.T) {
	s, sc, tok := testRefundRig(t)
	hooks := &fakeCounterHolds{tipOK: true}
	s.SetCounterHolds(hooks)
	r, _ := call(t, s.HandleKeyedPayment, sc, keyedHold("cnon:card-nonce-ok", "tip-k1", 2000, 0, true))
	a, err := callTip(t, s, sc, map[string]any{"square_payment_id": r.Payment.ID, "tip_cents": 400, "idempotency_key": "dt-h1"})
	if err != nil || !a.OK || a.TipCents != 400 || hooks.tips[r.Payment.ID] != 400 {
		t.Fatalf("tip on a hold = %+v, %v (kept %v)", a, err, hooks.tips)
	}
	got, _ := s.client.GetPayment(context.Background(), tok, r.Payment.ID)
	if currentTip(got) != 0 || got.Status != square.StatusApproved {
		t.Fatalf("Square changed before the capture: %+v", got)
	}
	// No waiting hold for it (the hook says no): Square directly, as batch F.
	hooks.tipOK = false
	b, err := callTip(t, s, sc, map[string]any{"square_payment_id": r.Payment.ID, "tip_cents": 500, "idempotency_key": "dt-h2"})
	if err != nil || !b.OK {
		t.Fatalf("no hold row: %+v, %v", b, err)
	}
	if got, _ := s.client.GetPayment(context.Background(), tok, r.Payment.ID); currentTip(got) != 500 {
		t.Fatalf("on Square: %+v", got)
	}
}
