package cardhold

import (
	"context"
	"testing"
	"time"

	"quiccpos/main/internal/domain/payment"
	"quiccpos/main/internal/infra/database/repositories"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// Counter delivery holds (agent contract §2.10ao).

type fakeCounterRepo struct {
	*fakeRepo
	out      repositories.CounterHoldOut
	inserted []repositories.CounterHoldIn
	cleared  int
}

func (f *fakeCounterRepo) InsertCounterHold(_ context.Context, h repositories.CounterHoldIn) (repositories.CounterHoldOut, error) {
	f.inserted = append(f.inserted, h)
	return f.out, nil
}
func (f *fakeCounterRepo) CounterHoldTip(context.Context, uuid.UUID, string, int64) (bool, error) {
	return true, nil
}
func (f *fakeCounterRepo) CounterHoldReleased(context.Context, uuid.UUID, string, string) (bool, error) {
	return true, nil
}
func (f *fakeCounterRepo) CounterHoldRefunded(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, int64, string) (bool, error) {
	return true, nil
}
func (f *fakeCounterRepo) ClearUndo(context.Context, uuid.UUID, string) error { f.cleared++; return nil }

func counterSvc(r Repo, g payment.Holder) *Service {
	return NewService(r, map[string]payment.Holder{"square": g, payment.GatewaySquareCounter: g}, nil, zerolog.Nop())
}

// A cancelled order whose counter hold was already captured is never
// refunded by main: the Pi gave the money back itself (refund first, through
// square.refund). The owed refund is just cleared.
func TestCancelledCounterHoldAfterCaptureIsNotRefundedTwice(t *testing.T) {
	h := hold("captured")
	h.Gateway, h.CapturedCents = payment.GatewaySquareCounter, 2300
	r := &fakeCounterRepo{fakeRepo: &fakeRepo{h: h, undo: true}}
	g := &fakeGW{}
	counterSvc(r, g).TickUndo(context.Background())
	if len(g.refunds) != 0 || len(r.refunded) != 0 || r.cleared != 1 {
		t.Fatalf("refunds %v recorded %v cleared %d (want none, none, 1)", g.refunds, r.refunded, r.cleared)
	}
}

// A cancelled order's waiting counter hold is voided like a website hold.
func TestCancelledCounterHoldWaitingIsVoided(t *testing.T) {
	h := hold("held")
	h.Gateway = payment.GatewaySquareCounter
	r := &fakeCounterRepo{fakeRepo: &fakeRepo{h: h, undo: true}}
	g := &fakeGW{}
	counterSvc(r, g).TickUndo(context.Background())
	if g.voids != 1 || r.cancelled != 1 || g.completes != 0 {
		t.Fatalf("voids %d cancelled %d completes %d", g.voids, r.cancelled, g.completes)
	}
}

// The hold is recorded with the 24 h safety capture; a second held card on
// an order that has a hold (not tracked) is captured at once.
func TestCounterHoldRecordedAndAnUntrackedOneCaptured(t *testing.T) {
	r := &fakeCounterRepo{fakeRepo: &fakeRepo{}, out: repositories.CounterHoldOut{Inserted: true, Tracked: true, Delivery: true}}
	g := &fakeGW{}
	s := counterSvc(r, g)
	at := time.Date(2026, 11, 14, 18, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return at }
	p := CounterPayment{TenantID: uuid.New(), LocationID: uuid.New(), OrderID: uuid.New(), Ref: "sq_c1", AmountCents: 2000, TipCents: 0}
	s.CounterHold(context.Background(), p)
	if len(r.inserted) != 1 || !r.inserted[0].DueAt.Equal(at.Add(24*time.Hour)) || r.inserted[0].PaymentRef != "sq_c1" || g.completes != 0 {
		t.Fatalf("inserted %+v completes %d", r.inserted, g.completes)
	}
	r.out = repositories.CounterHoldOut{Delivery: true} // the order has another hold
	s.CounterHold(context.Background(), p)
	if g.completes != 1 {
		t.Fatalf("an untracked held card must be captured at once: completes %d", g.completes)
	}
}

// A counter hold the capture can't raise to the typed tip is captured
// without it and never charged on a saved card (gateway refuses: the
// counter gateway's ChargeTip is permanent).
func TestCounterCaptureWithATipSquareWontRaise(t *testing.T) {
	h := hold("held")
	h.Gateway, h.AddedTipCents = payment.GatewaySquareCounter, 400
	r := &fakeCounterRepo{fakeRepo: &fakeRepo{h: h, claim: true, saveOK: true}}
	g := &fakeGW{raiseErr: payment.ErrCantRaise}
	counterSvc(r, refusingTip{g}).Tick(context.Background())
	if r.captured[0] != 2300 || r.captured[2] != 0 {
		t.Fatalf("captured %v (want the hold alone, no separate tip)", r.captured)
	}
}

type refusingTip struct{ *fakeGW }

func (refusingTip) ChargeTip(context.Context, payment.Hold, int64, payment.Buyer) (string, error) {
	return "", payment.Permanent{Err: context.Canceled}
}
