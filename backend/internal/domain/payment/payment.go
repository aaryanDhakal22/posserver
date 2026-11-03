// Package payment is the payment port (main 3.0 step 5): a Gateway charges an
// order's total and refunds it. Adapters: cash (nothing charged — collected
// at the store or by the driver, today's pending cash payment) and the test
// card (dev / staging only, deterministic tokens). Square plugs in later.
package payment

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// Methods a channel order can be paid with.
const (
	MethodCash = "cash"
	MethodCard = "card"
)

var (
	ErrDeclined = errors.New("the card was declined")
	ErrBadToken = errors.New("no usable card token")
)

// Charge is what a gateway is asked to take.
type Charge struct {
	AmountCents    int64
	Token          string // the channel's card token (card)
	IdempotencyKey string // the same key never charges twice
}

// Result is a charge's outcome.
type Result struct {
	Gateway  string // which adapter took it
	Ref      string // the adapter's reference (refunds)
	Status   string // pending (cash) | captured
	Last4    string
	AuthCode string
}

// Gateway takes one payment method.
type Gateway interface {
	Charge(ctx context.Context, c Charge) (Result, error)
	Refund(ctx context.Context, ref string, amountCents int64) error
}

// Cash records the money as owed: nothing is charged now.
type Cash struct{}

func (Cash) Charge(_ context.Context, c Charge) (Result, error) {
	return Result{Gateway: "cash", Ref: "", Status: "pending"}, nil
}

func (Cash) Refund(context.Context, string, int64) error { return nil }

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
	r := Result{Gateway: "testcard", Ref: "tc_" + c.IdempotencyKey, Status: "captured", Last4: "4242", AuthCode: "TEST01"}
	if c.IdempotencyKey != "" {
		t.charges[c.IdempotencyKey] = r
	}
	return r, nil
}

func (t *TestCard) Refund(_ context.Context, ref string, amountCents int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refunds[ref] += amountCents
	return nil
}

// Refunded is how much was refunded on a charge ref.
func (t *TestCard) Refunded(ref string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.refunds[ref]
}
