package squarepay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"quiccpos/main/internal/infra/square"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
)

// A general refund to a card (batch J, agent contract §2.10x, main §2.5e) on
// the fake Square: the unlinked refund, its refusals, and the fallback's list
// of the store's card payments followed by an ordinary linked refund.

func generalRig(t *testing.T) (*Service, sseBroker.Scope, string, *bytes.Buffer) {
	t.Helper()
	s, repo, loc, logs, _ := oauthRig(t)
	code, state := authorize(t, s, loc.ID)
	if _, _, err := s.Finish(context.Background(), code, state, ""); err != nil {
		t.Fatal(err)
	}
	s.repo = connected{repo}
	tok, _, err := s.conn(context.Background(), loc.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, sseBroker.Scope{TenantID: loc.TenantID, LocationID: loc.ID}, tok, logs
}

func generalRefund(t *testing.T, s *Service, sc sseBroker.Scope, body any) (GeneralRefundReply, error) {
	t.Helper()
	raw, _ := json.Marshal(body)
	out, err := s.HandleGeneralRefund(context.Background(), sc, raw)
	if err != nil {
		return GeneralRefundReply{}, err
	}
	return out.(GeneralRefundReply), nil
}

func cardPayments(t *testing.T, s *Service, sc sseBroker.Scope, body any) (CardPaymentsReply, error) {
	t.Helper()
	raw, _ := json.Marshal(body)
	out, err := s.HandleCardPayments(context.Background(), sc, raw)
	if err != nil {
		return CardPaymentsReply{}, err
	}
	return out.(CardPaymentsReply), nil
}

func gr(nonce, key string, cents int64) map[string]any {
	return map[string]any{"nonce": nonce, "amount_cents": cents, "idempotency_key": key, "reason": "Overcharged yesterday"}
}

func TestGeneralRefundIsAnUnlinkedRefundAndIdempotent(t *testing.T) {
	s, sc, _, logs := generalRig(t)
	r, err := generalRefund(t, s, sc, gr("cnon:card-nonce-ok", "gr-key-1", 750))
	if err != nil || !r.OK || r.RefundID == "" || r.Status != "PENDING" || !r.Unlinked || r.Brand != "VISA" || r.Last4 != "1111" {
		t.Fatalf("unlinked refund = %+v, %v", r, err)
	}
	// The same key again (a lost answer): the same refund, never a second one.
	again, err := generalRefund(t, s, sc, gr("cnon:card-nonce-ok", "gr-key-1", 750))
	if err != nil || !again.OK || again.RefundID != r.RefundID {
		t.Fatalf("again = %+v, %v (first %s)", again, err, r.RefundID)
	}
	// The same key with another card: Square refuses it (never paid twice).
	other, err := generalRefund(t, s, sc, gr("cnon:card-nonce-no-edit", "gr-key-1", 750))
	if err != nil || other.OK || other.Code != CodeRefundFailed || other.SquareCode != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("key reused with another card = %+v, %v", other, err)
	}
	if strings.Contains(logs.String(), "cnon:") {
		t.Fatal("the card token reached the log")
	}
}

func TestGeneralRefundRefusals(t *testing.T) {
	s, sc, _, _ := generalRig(t)
	// An unknown / used token: refund_failed (Square's NOT_FOUND).
	r, err := generalRefund(t, s, sc, gr("cnon:nobody", "gr-k2", 100))
	if err != nil || r.OK || r.Code != CodeRefundFailed || r.SquareCode != "NOT_FOUND" {
		t.Fatalf("unknown token = %+v, %v", r, err)
	}
	// A seller Square won't make unlinked refunds for: the terminal offers the fallback.
	r, err = generalRefund(t, s, sc, gr("cnon:unlinked-refund-unsupported", "gr-k3", 100))
	if err != nil || r.OK || r.Code != CodeUnlinkedUnsupported {
		t.Fatalf("unsupported = %+v, %v", r, err)
	}
	// Square's 503: an error (main answers 500: not known, the same key again is safe).
	fakeFail(t, s, "refund", "TEMPORARY_ERROR", 503)
	if _, err := generalRefund(t, s, sc, gr("cnon:card-nonce-ok", "gr-k4", 100)); err == nil {
		t.Fatal("a 503 from Square must be an error (outcome unknown)")
	}
	// The same key after the 503: one refund.
	ok1, err := generalRefund(t, s, sc, gr("cnon:card-nonce-ok", "gr-k4", 100))
	if err != nil || !ok1.OK {
		t.Fatalf("retry after 503 = %+v, %v", ok1, err)
	}
	// Not linked.
	nl, err := generalRefund(t, s, sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}, gr("cnon:card-nonce-ok", "gr-k5", 100))
	if err != nil || nl.OK || nl.Code != CodeNotLinked {
		t.Fatalf("not linked = %+v, %v", nl, err)
	}
	// Bad payloads: 400.
	for _, b := range []map[string]any{
		gr("", "k", 100), gr("cnon:card-nonce-ok", "", 100), gr("cnon:card-nonce-ok", "k", 0),
		gr("cnon:card-nonce-ok", "k", 1_000_001), gr("cnon:card-nonce-ok", strings.Repeat("k", 46), 100),
	} {
		if _, err := generalRefund(t, s, sc, b); !errors.Is(err, rpc.ErrBadPayload) {
			t.Fatalf("%v = %v, want a bad payload", b, err)
		}
	}
}

func TestCardPaymentsFallbackListsMatchesThenALinkedRefund(t *testing.T) {
	s, sc, tok, _ := generalRig(t)
	small := pay(t, s, tok, 300, true)  // under the amount
	big := pay(t, s, tok, 2000, true)   // holds it
	partly := pay(t, s, tok, 900, true) // 900 − 600 refunded: under
	held := pay(t, s, tok, 5000, false) // a hold (APPROVED): never listed
	if _, err := s.client.RefundPayment(context.Background(), tok, "pre-"+partly, partly, square.USD(600), ""); err != nil {
		t.Fatal(err)
	}
	r, err := cardPayments(t, s, sc, map[string]any{"last4": "1111", "amount_cents": 500})
	if err != nil || !r.OK {
		t.Fatalf("card payments = %+v, %v", r, err)
	}
	if len(r.Payments) != 1 || r.Payments[0].SquarePaymentID != big {
		t.Fatalf("listed %+v, want only %s (not %s, %s, %s)", r.Payments, big, small, partly, held)
	}
	m := r.Payments[0]
	if m.AmountCents != 2000 || m.RefundableCents != 2000 || m.Last4 != "1111" || m.Brand != "VISA" || m.At.IsZero() || m.Entry == "" {
		t.Fatalf("match %+v", m)
	}
	// Another card's last 4: nothing.
	if none, err := cardPayments(t, s, sc, map[string]any{"last4": "4242", "amount_cents": 500}); err != nil || !none.OK || len(none.Payments) != 0 {
		t.Fatalf("other card = %+v, %v", none, err)
	}
	// The manager picks it: an ordinary linked square.refund.
	ref, err := call(t, s.HandleRefund, sc, map[string]any{"payment_id": m.SquarePaymentID, "amount_cents": 500, "idempotency_key": "gr-linked-1", "reason": "Overcharged"})
	if err != nil || !ref.OK || ref.RefundID == "" {
		t.Fatalf("linked refund = %+v, %v", ref, err)
	}
	// It now holds 1500: a 1600 refund lists nothing.
	if left, _ := cardPayments(t, s, sc, map[string]any{"last4": "1111", "amount_cents": 1600}); len(left.Payments) != 0 {
		t.Fatalf("after the refund: %+v", left.Payments)
	}
	// Square's 503 on the list: an error; bad bodies: 400; not linked: an answer.
	fakeFail(t, s, "list", "TEMPORARY_ERROR", 503)
	if _, err := cardPayments(t, s, sc, map[string]any{"last4": "1111", "amount_cents": 500}); err == nil {
		t.Fatal("a 503 on the list must be an error")
	}
	for _, b := range []map[string]any{{"last4": "111", "amount_cents": 1}, {"last4": "abcd", "amount_cents": 1}, {"last4": "1111", "amount_cents": 0}} {
		if _, err := cardPayments(t, s, sc, b); !errors.Is(err, rpc.ErrBadPayload) {
			t.Fatalf("%v = %v", b, err)
		}
	}
	nl, err := cardPayments(t, s, sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}, map[string]any{"last4": "1111", "amount_cents": 1})
	if err != nil || nl.OK || nl.Code != CodeNotLinked {
		t.Fatalf("not linked = %+v, %v", nl, err)
	}
}
