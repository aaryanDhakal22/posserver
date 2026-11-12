package squarepay

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"quiccpos/main/internal/infra/database/repositories"
	"quiccpos/main/internal/infra/square"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// connected plays a store that connected Square: the OAuth finish stores the
// connection, Get reads it back.
type connected struct{ *upserts }

func (c connected) Get(_ context.Context, id uuid.UUID) (repositories.SquareConnection, error) {
	for _, x := range c.got {
		if x.LocationID == id {
			return x, nil
		}
	}
	return repositories.SquareConnection{}, pgx.ErrNoRows
}

func (c connected) UpdateTokens(_ context.Context, id uuid.UUID, access, refresh []byte, expires time.Time) error {
	for i := range c.got {
		if c.got[i].LocationID == id {
			c.got[i].AccessEnc, c.got[i].RefreshEnc, c.got[i].ExpiresAt, c.got[i].RefreshedAt = access, refresh, expires, time.Now()
		}
	}
	return nil
}

func testRefundRig(t *testing.T) (*Service, sseBroker.Scope, string) {
	t.Helper()
	s, repo, loc, _, _ := oauthRig(t)
	code, state := authorize(t, s, loc.ID)
	if _, _, err := s.Finish(context.Background(), code, state, ""); err != nil {
		t.Fatal(err)
	}
	s.repo = connected{repo}
	tok, sqLoc, err := s.conn(context.Background(), loc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sqLoc == "" {
		t.Fatal("no Square location")
	}
	return s, sseBroker.Scope{TenantID: loc.TenantID, LocationID: loc.ID}, tok
}

func pay(t *testing.T, s *Service, tok string, cents int64, complete bool) string {
	t.Helper()
	p, err := s.client.CreatePayment(context.Background(), tok, square.CreatePayment{
		IdempotencyKey: uuid.NewString(), SourceID: "cnon:card-nonce-no-edit", AmountMoney: square.USD(cents), Autocomplete: complete,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func testRefund(t *testing.T, s *Service, sc sseBroker.Scope, body map[string]any) (TestRefundReply, error) {
	t.Helper()
	raw, _ := json.Marshal(body)
	out, err := s.HandleTestRefund(context.Background(), sc, raw)
	if err != nil {
		return TestRefundReply{}, err
	}
	return out.(TestRefundReply), nil
}

func TestTestRefundGivesTheDollarBack(t *testing.T) {
	s, sc, tok := testRefundRig(t)
	id := pay(t, s, tok, 100, true)
	body := map[string]any{"square_payment_id": id, "amount_cents": 100, "idempotency_key": "setup-test-1"}
	r, err := testRefund(t, s, sc, body)
	if err != nil || !r.OK || r.RefundID == "" || r.Status == "" {
		t.Fatalf("refund = %+v, %v", r, err)
	}
	// The same key again is the same refund (the terminal's retry).
	again, err := testRefund(t, s, sc, body)
	if err != nil || !again.OK || again.RefundID != r.RefundID {
		t.Fatalf("retry = %+v, %v (first %s)", again, err, r.RefundID)
	}
}

func TestTestRefundCancelsAnUncapturedCharge(t *testing.T) {
	s, sc, tok := testRefundRig(t)
	id := pay(t, s, tok, 100, false)
	r, err := testRefund(t, s, sc, map[string]any{"square_payment_id": id, "amount_cents": 100, "idempotency_key": "k"})
	if err != nil || !r.OK || r.Status != "CANCELED" {
		t.Fatalf("cancel = %+v, %v", r, err)
	}
}

func TestTestRefundRefusesASale(t *testing.T) {
	s, sc, tok := testRefundRig(t)
	id := pay(t, s, tok, 2500, true) // a real sale, not the $1 test
	r, err := testRefund(t, s, sc, map[string]any{"square_payment_id": id, "amount_cents": 100, "idempotency_key": "k"})
	if err != nil || r.OK || r.Code != "not_a_test_charge" {
		t.Fatalf("sale = %+v, %v", r, err)
	}
	r, err = testRefund(t, s, sc, map[string]any{"square_payment_id": "pay_nope", "amount_cents": 100, "idempotency_key": "k"})
	if err != nil || r.OK || r.Code != "not_a_test_charge" {
		t.Fatalf("unknown payment = %+v, %v", r, err)
	}
}

func TestTestRefundBadBodies(t *testing.T) {
	s, sc, _ := testRefundRig(t)
	for name, b := range map[string]map[string]any{
		"no payment":   {"amount_cents": 100, "idempotency_key": "k"},
		"zero":         {"square_payment_id": "p", "amount_cents": 0, "idempotency_key": "k"},
		"over $1":      {"square_payment_id": "p", "amount_cents": 101, "idempotency_key": "k"},
		"no key":       {"square_payment_id": "p", "amount_cents": 100},
		"key too long": {"square_payment_id": "p", "amount_cents": 100, "idempotency_key": "0123456789012345678901234567890123456789012345"},
	} {
		if _, err := testRefund(t, s, sc, b); !errors.Is(err, rpc.ErrBadPayload) {
			t.Errorf("%s: %v, want a bad payload", name, err)
		}
	}
	if _, err := s.HandleTestRefund(context.Background(), sc, json.RawMessage(`nope`)); !errors.Is(err, rpc.ErrBadPayload) {
		t.Errorf("not JSON: %v", err)
	}
}

func TestTestRefundNotLinked(t *testing.T) {
	s, sc, _ := testRefundRig(t)
	other := sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}
	r, err := testRefund(t, s, other, map[string]any{"square_payment_id": "p", "amount_cents": 100, "idempotency_key": "k"})
	if err != nil || r.OK || r.Code != "square_not_linked" {
		t.Fatalf("not linked = %+v, %v", r, err)
	}
	_ = sc
}
