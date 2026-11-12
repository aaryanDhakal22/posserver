package cardhold

import (
	"context"
	"errors"
	"testing"
	"time"

	"quiccpos/main/internal/domain/payment"
	"quiccpos/main/internal/infra/database/repositories"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// fakeRepo keeps one hold in memory and records what the service wrote.
type fakeRepo struct {
	Repo
	h          repositories.CardHold
	undo       bool // ClaimUndo hands the hold out (once per call)
	claim      bool // Claim hands the hold out
	retries    int
	failed     int
	cancelled  int
	refunded   []int64
	captured   []int64 // onHold, tip, separate
	saveOK     bool
	tipCap     int64
	setTip     int64
	cancelErrs []error
}

func (f *fakeRepo) ClaimUndo(context.Context, int) ([]repositories.CardHold, error) {
	if !f.undo {
		return nil, nil
	}
	f.undo = false
	return []repositories.CardHold{f.h}, nil
}
func (f *fakeRepo) Claim(context.Context, int) ([]repositories.CardHold, error) {
	if !f.claim {
		return nil, nil
	}
	f.claim = false
	return []repositories.CardHold{f.h}, nil
}
func (f *fakeRepo) Retry(_ context.Context, _ uuid.UUID, _ time.Time, _ string) error {
	f.retries++
	f.h.Attempts++
	return nil
}
func (f *fakeRepo) UndoFailed(context.Context, uuid.UUID, string) error { f.failed++; return nil }
func (f *fakeRepo) Failed(context.Context, uuid.UUID, string) error     { f.failed++; return nil }
func (f *fakeRepo) Cancelled(context.Context, uuid.UUID, string) error {
	if len(f.cancelErrs) > 0 {
		err := f.cancelErrs[0]
		f.cancelErrs = f.cancelErrs[1:]
		return err
	}
	f.cancelled++
	f.h.Status = "cancelled"
	return nil
}
func (f *fakeRepo) Refunded(_ context.Context, _ repositories.CardHold, cents int64, _ string, tip int64, _, _ string) error {
	f.refunded = append(f.refunded, cents, tip)
	return nil
}
func (f *fakeRepo) Captured(_ context.Context, _ repositories.CardHold, onHold, tip, separate int64, _, _ string, _ int64) (bool, error) {
	f.captured = []int64{onHold, tip, separate}
	return f.saveOK, nil
}
func (f *fakeRepo) TipCap(context.Context, uuid.UUID) (int64, error) { return f.tipCap, nil }
func (f *fakeRepo) SetAddedTip(_ context.Context, _, _ uuid.UUID, cents int64) (bool, error) {
	f.setTip = cents
	return true, nil
}

// fakeGW is a processor whose calls can fail.
type fakeGW struct {
	voidErrs  []error
	voids     int
	refunds   []string
	completes int
	raiseErr  error
	chargeTip string
}

func (g *fakeGW) Charge(context.Context, payment.Charge) (payment.Result, error) {
	return payment.Result{}, nil
}
func (g *fakeGW) Refund(_ context.Context, _ uuid.UUID, ref string, _ int64) error {
	g.refunds = append(g.refunds, ref)
	return nil
}
func (g *fakeGW) RaiseTip(context.Context, payment.Hold, int64) error { return g.raiseErr }
func (g *fakeGW) Complete(_ context.Context, h payment.Hold) (int64, error) {
	g.completes++
	return h.AmountCents + h.TipCents, nil
}
func (g *fakeGW) Void(context.Context, payment.Hold) error {
	g.voids++
	if len(g.voidErrs) > 0 {
		err := g.voidErrs[0]
		g.voidErrs = g.voidErrs[1:]
		return err
	}
	return nil
}
func (g *fakeGW) ChargeTip(context.Context, payment.Hold, int64, payment.Buyer) (string, error) {
	return g.chargeTip, nil
}

func newSvc(r *fakeRepo, g *fakeGW) *Service {
	return NewService(r, map[string]payment.Holder{"square": g}, nil, zerolog.Nop())
}

func hold(status string) repositories.CardHold {
	return repositories.CardHold{OrderID: uuid.New(), LocationID: uuid.New(), Gateway: "square", PaymentRef: "sq_1",
		AmountCents: 2000, TipCents: 300, Status: status}
}

// M-3: a void that fails is retried with backoff (never dropped, the hold
// stays out of the capture queue); the next try voids it.
func TestCancelledHoldVoidRetries(t *testing.T) {
	r := &fakeRepo{h: hold("held"), undo: true}
	g := &fakeGW{voidErrs: []error{errors.New("square is down")}}
	s := newSvc(r, g)
	s.TickUndo(context.Background())
	if g.voids != 1 || r.retries != 1 || r.cancelled != 0 || r.failed != 0 {
		t.Fatalf("first try: voids %d retries %d cancelled %d failed %d", g.voids, r.retries, r.cancelled, r.failed)
	}
	r.undo = true
	s.TickUndo(context.Background())
	if g.voids != 2 || r.cancelled != 1 || g.completes != 0 {
		t.Fatalf("second try: voids %d cancelled %d completes %d", g.voids, r.cancelled, g.completes)
	}
}

// M-3: a void that keeps failing gives up after MaxAttempts and pages
// (UndoFailed), never captures.
func TestCancelledHoldVoidGivesUp(t *testing.T) {
	r := &fakeRepo{h: hold("held"), undo: true}
	r.h.Attempts = MaxAttempts - 1
	g := &fakeGW{voidErrs: []error{errors.New("still down")}}
	newSvc(r, g).TickUndo(context.Background())
	if r.failed != 1 || r.retries != 0 || g.completes != 0 {
		t.Fatalf("give up: failed %d retries %d completes %d", r.failed, r.retries, g.completes)
	}
}

// M-3: a hold captured before the cancel is refunded (its separate tip too).
func TestCancelledAfterCaptureRefunds(t *testing.T) {
	h := hold("captured")
	h.CapturedCents, h.AddedTipCents, h.TipPaymentRef = 2700, 400, "sq_tip"
	r := &fakeRepo{h: h, undo: true}
	g := &fakeGW{}
	newSvc(r, g).TickUndo(context.Background())
	if len(g.refunds) != 2 || g.refunds[0] != "sq_1" || g.refunds[1] != "sq_tip" {
		t.Fatalf("refunds: %v", g.refunds)
	}
	if len(r.refunded) != 2 || r.refunded[0] != 2300 || r.refunded[1] != 400 {
		t.Fatalf("recorded: %v", r.refunded)
	}
}

// M-14: a tip set while the capture ran makes the save a no-op; the hold is
// requeued (looked at again), not lost.
func TestCaptureRaceRequeues(t *testing.T) {
	r := &fakeRepo{h: hold("held"), claim: true, saveOK: false}
	g := &fakeGW{}
	newSvc(r, g).Tick(context.Background())
	if g.completes != 1 || r.retries != 1 {
		t.Fatalf("completes %d retries %d", g.completes, r.retries)
	}
	if r.captured[0] != 2300 || r.captured[1] != 300 || r.captured[2] != 0 {
		t.Fatalf("captured: %v", r.captured)
	}
}

// M-14: a late tip the hold can't carry is charged separately and recorded
// as its own amount (not folded into the hold's payment row).
func TestCaptureSeparateTip(t *testing.T) {
	h := hold("held")
	h.AddedTipCents = 500
	r := &fakeRepo{h: h, claim: true, saveOK: true}
	g := &fakeGW{raiseErr: payment.ErrCantRaise, chargeTip: "sq_tip"}
	newSvc(r, g).Tick(context.Background())
	if r.captured[0] != 2300 || r.captured[1] != 300 || r.captured[2] != 500 {
		t.Fatalf("captured: %v (on hold, tip on hold, separate)", r.captured)
	}
}

// M-20: an added tip over the store's cap is refused.
func TestSetTipCap(t *testing.T) {
	r := &fakeRepo{tipCap: 5000}
	s := newSvc(r, &fakeGW{})
	if _, err := s.SetTip(context.Background(), uuid.New(), uuid.New(), 5001); !errors.Is(err, ErrTipTooBig) {
		t.Fatalf("over the cap: %v", err)
	}
	if ok, err := s.SetTip(context.Background(), uuid.New(), uuid.New(), 5000); err != nil || !ok || r.setTip != 5000 {
		t.Fatalf("at the cap: %v %v %d", ok, err, r.setTip)
	}
}
