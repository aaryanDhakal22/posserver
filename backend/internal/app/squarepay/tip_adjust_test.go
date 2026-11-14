package squarepay

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"quiccpos/main/internal/infra/square"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
)

func callTip(t *testing.T, s *Service, sc sseBroker.Scope, body any) (TipAdjustReply, error) {
	t.Helper()
	raw, _ := json.Marshal(body)
	out, err := s.HandleTipAdjust(context.Background(), sc, raw)
	if err != nil {
		return TipAdjustReply{}, err
	}
	return out.(TipAdjustReply), nil
}

// Batch F (#25, agent contract §2.10am): the press of Close batch puts the
// driver's typed card tip on the Square payment - an APPROVED (delayed
// capture) payment takes it; a completed one is not_adjustable.
func TestTipAdjustPutsTheTipOnTheSquarePayment(t *testing.T) {
	s, sc, tok := testRefundRig(t)
	p, err := s.client.CreatePayment(context.Background(), tok, square.CreatePayment{
		IdempotencyKey: uuid.NewString(), SourceID: "cnon:card-nonce-ok", AmountMoney: square.USD(2000), Autocomplete: false})
	if err != nil || p.Status != square.StatusApproved {
		t.Fatalf("approved payment: %+v %v", p, err)
	}
	r, err := callTip(t, s, sc, map[string]any{"square_payment_id": p.ID, "tip_cents": 400, "idempotency_key": "dt-1", "order_id": "o-1"})
	if err != nil || !r.OK || r.TipCents != 400 {
		t.Fatalf("adjust = %+v, %v", r, err)
	}
	got, err := s.client.GetPayment(context.Background(), tok, p.ID)
	if err != nil || currentTip(got) != 400 {
		t.Fatalf("on Square: %+v %v", got, err)
	}
	// The same whole tip again: nothing to do, ok. Lowered: Square takes it.
	if again, err := callTip(t, s, sc, map[string]any{"square_payment_id": p.ID, "tip_cents": 400, "idempotency_key": "dt-1"}); err != nil || !again.OK {
		t.Fatalf("again = %+v, %v", again, err)
	}
	if low, err := callTip(t, s, sc, map[string]any{"square_payment_id": p.ID, "tip_cents": 250, "idempotency_key": "dt-2"}); err != nil || !low.OK {
		t.Fatalf("lowered = %+v, %v", low, err)
	}
	// A completed payment (the counter's reader / typed card today) takes no tip change.
	r2, _ := call(t, s.HandleKeyedPayment, sc, keyed("cnon:card-nonce-ok", "pay-tip-2", 1500, 0))
	done, err := callTip(t, s, sc, map[string]any{"square_payment_id": r2.Payment.ID, "tip_cents": 300, "idempotency_key": "dt-3"})
	if err != nil || done.OK || done.Code != CodeNotAdjustable || done.SquareCode != square.StatusCompleted {
		t.Fatalf("completed = %+v, %v", done, err)
	}
	unk, err := callTip(t, s, sc, map[string]any{"square_payment_id": "pay_nope", "tip_cents": 300, "idempotency_key": "dt-4"})
	if err != nil || unk.OK || unk.Code != "square_refused" {
		t.Fatalf("unknown = %+v, %v", unk, err)
	}
	if _, err := callTip(t, s, sc, map[string]any{"square_payment_id": p.ID, "tip_cents": -1, "idempotency_key": "dt-5"}); !errors.Is(err, rpc.ErrBadPayload) {
		t.Fatalf("bad body: %v", err)
	}
	nl, err := callTip(t, s, sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}, map[string]any{"square_payment_id": p.ID, "tip_cents": 1, "idempotency_key": "k"})
	if err != nil || nl.Code != CodeNotLinked {
		t.Fatalf("not linked = %+v, %v", nl, err)
	}
}
