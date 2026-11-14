// Package payment is the payment port (main 3.0 step 5): a Gateway charges an
// order's total and refunds it. Adapters: cash (nothing charged — collected
// at the store or by the driver, today's pending cash payment), the test
// card (dev / staging only, deterministic tokens) and Square (weborder stage
// 2: a Holder — the card is authorized at checkout and captured later).
package payment

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Methods a channel order can be paid with.
const (
	MethodCash = "cash"
	MethodCard = "card"
)

var (
	ErrDeclined = errors.New("the card was declined")
	ErrBadToken = errors.New("no usable card token")
	// ErrVerify: the card needs the buyer to verify (3-D Secure) first.
	ErrVerify = errors.New("the card needs verification")
	// ErrNotConnected: the store takes no cards (no Square connected).
	ErrNotConnected = errors.New("card payments aren't set up for this store")
	// ErrCantRaise: the hold can't grow (Square refused a bigger tip).
	ErrCantRaise = errors.New("the card hold can't be raised")
	// ErrHoldGone: the hold was voided / expired at the processor.
	ErrHoldGone = errors.New("the card hold is no longer there")
)

// Payment statuses a Result can carry.
const (
	StatusPending    = "pending"    // cash: collected at the store / by the driver
	StatusAuthorized = "authorized" // a hold, captured later (Holder)
	StatusCaptured   = "captured"
)

// Charge is what a gateway is asked to take.
type Charge struct {
	LocationID        uuid.UUID // the store (its processor account)
	AmountCents       int64     // the whole total, tip included
	TipCents          int64     // the tip inside AmountCents
	Token             string    // the channel's card token (card)
	VerificationToken string    // the buyer's verification (3-D Secure), if any
	IdempotencyKey    string    // the same key never charges twice
	ReferenceID       string    // shown on the processor's side (the channel's order id)
	Note              string
	BuyerEmail        string
	// Saved cards (weborder stage 2, signed-in customers): CardRef pays with
	// a card saved earlier (instead of Token); SaveCard keeps the Token's card
	// for next time. CustomerRef is the processor's customer ("" = make one
	// from Buyer when a card is saved).
	CardRef     string
	CustomerRef string
	SaveCard    bool
	Buyer       Buyer
}

// SavedCard is a card the processor kept for the customer.
type SavedCard struct {
	Ref               string
	Brand, Last4      string
	ExpMonth, ExpYear int
}

// Result is a charge's outcome.
type Result struct {
	Gateway  string // which adapter took it
	Ref      string // the adapter's reference (refunds)
	Status   string // pending (cash) | authorized (a hold) | captured
	Last4    string
	Brand    string
	AuthCode string
	// Saved is the card kept for next time (SaveCard); CustomerRef the
	// processor's customer it belongs to.
	Saved       *SavedCard
	CustomerRef string
}

// Gateway takes one payment method.
type Gateway interface {
	Charge(ctx context.Context, c Charge) (Result, error)
	// Refund gives money back (a hold not captured yet is voided instead).
	Refund(ctx context.Context, locationID uuid.UUID, ref string, amountCents int64) error
}

// Hold is an authorized card payment waiting to be captured.
// KeyVoider is an optional Gateway upgrade: VoidByKey releases whatever a
// Charge with that idempotency key made, without making anything (a
// placement that expired after its card call — DURABILITY-PLAN.md). No
// payment for the key is not an error.
type KeyVoider interface {
	VoidByKey(ctx context.Context, locationID uuid.UUID, idempotencyKey string) error
}

// ErrCanceled: the key's payment exists but was canceled (a retry can't
// revive it — the channel places the order again under a new key).
var ErrCanceled = errors.New("the card payment for this order was canceled")

type Hold struct {
	OrderID     uuid.UUID
	LocationID  uuid.UUID
	Ref         string
	AmountCents int64 // the hold without its tip
	TipCents    int64 // the tip on the hold
}

// Buyer is who a saved card belongs to (the order's customer).
type Buyer struct {
	Name, Phone, Email string
}

// GatewaySquareCounter labels a counter delivery's Square card hold
// (card_holds.gateway, agent contract §2.10ao): taken at the store by the call
// center, Manual Entry or the reader, held so the driver's typed tip goes on
// it, captured by the same hold machine as a website hold. Its tip is never
// charged on a saved card, and once captured the Pi gives refunds back itself.
const GatewaySquareCounter = "square_counter"

// Holder is a gateway whose card charges are holds (Square): captured with
// the final tip at the driver's clock-out / Close batch, or voided.
type Holder interface {
	Gateway
	// RaiseTip sets the hold's tip (ErrCantRaise when the card won't grow).
	RaiseTip(ctx context.Context, h Hold, tipCents int64) error
	// Complete captures the hold; it returns what was charged.
	Complete(ctx context.Context, h Hold) (int64, error)
	// Void cancels a hold (nothing is charged).
	Void(ctx context.Context, h Hold) error
	// ChargeTip charges a tip the hold couldn't carry on the card behind the
	// (completed) hold: the card is saved for this order, charged, then
	// turned off. It returns the tip payment's reference.
	ChargeTip(ctx context.Context, h Hold, cents int64, b Buyer) (string, error)
}

// Permanent wraps an error retrying won't fix.
type Permanent struct{ Err error }

func (p Permanent) Error() string { return p.Err.Error() }
func (p Permanent) Unwrap() error { return p.Err }

// IsPermanent reports whether retrying err is pointless.
func IsPermanent(err error) bool {
	var p Permanent
	return errors.As(err, &p) || errors.Is(err, ErrHoldGone) || errors.Is(err, ErrNotConnected)
}

// Cash records the money as owed: nothing is charged now.
type Cash struct{}

func (Cash) Charge(_ context.Context, c Charge) (Result, error) {
	return Result{Gateway: "cash", Ref: "", Status: StatusPending}, nil
}

func (Cash) Refund(context.Context, uuid.UUID, string, int64) error { return nil }

// TestCard is a fake card processor for dev and staging: token tok_visa (or
// any tok_ok_*) is approved, tok_decline is declined, anything else is a bad
// token. Charges are remembered by idempotency key and refunds by ref, so
// tests can check both.
type TestCard struct {
	mu      sync.Mutex
	charges map[string]Result
	refunds map[string]int64
}

func NewTestCard() *TestCard {
	return &TestCard{charges: map[string]Result{}, refunds: map[string]int64{}}
}

func (t *TestCard) Charge(_ context.Context, c Charge) (Result, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r, ok := t.charges[c.IdempotencyKey]; ok && c.IdempotencyKey != "" {
		return r, nil
	}
	switch {
	case c.Token == "tok_decline":
		return Result{}, ErrDeclined
	case c.Token == "tok_visa" || strings.HasPrefix(c.Token, "tok_ok_"):
	default:
		return Result{}, ErrBadToken
	}
	r := Result{Gateway: "testcard", Ref: "tc_" + c.IdempotencyKey, Status: StatusCaptured, Last4: "4242", Brand: "VISA", AuthCode: "TEST01"}
	if c.IdempotencyKey != "" {
		t.charges[c.IdempotencyKey] = r
	}
	return r, nil
}

func (t *TestCard) Refund(_ context.Context, _ uuid.UUID, ref string, amountCents int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refunds[ref] += amountCents
	return nil
}

// VoidByKey refunds the test card's charge made with that key, if any.
func (t *TestCard) VoidByKey(_ context.Context, _ uuid.UUID, key string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r, ok := t.charges[key]; ok {
		t.refunds[r.Ref] += 1 // marked: nothing was captured for real
	}
	return nil
}

// Refunded is how much was refunded on a charge ref.
func (t *TestCard) Refunded(ref string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.refunds[ref]
}
