package squarepay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"quiccpos/main/internal/infra/database/repositories"
	"quiccpos/main/internal/infra/square"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
)

// Square on the counter terminal (agent contract §2.10r) on the fake Square:
// the reader's session, a typed card's payment, refunds.

func call(t *testing.T, h func(context.Context, sseBroker.Scope, json.RawMessage) (any, error), sc sseBroker.Scope, body any) (TerminalReply, error) {
	t.Helper()
	raw, _ := json.Marshal(body)
	out, err := h(context.Background(), sc, raw)
	if err != nil {
		return TerminalReply{}, err
	}
	return out.(TerminalReply), nil
}

func TestSessionHandsOutTheStoresToken(t *testing.T) {
	s, sc, tok := testRefundRig(t)
	r, err := call(t, s.HandleSession, sc, map[string]any{})
	if err != nil || !r.OK {
		t.Fatalf("session = %+v, %v", r, err)
	}
	if r.AccessToken != tok || r.LocationID == "" || r.Environment != "fake" || r.AppID != "sq0idp-fake" || r.ExpiresAt == nil {
		t.Fatalf("session = %+v (token %s)", r, tok)
	}
	if r.InPerson == nil || !*r.InPerson {
		t.Fatalf("in_person = %v, want true (main asks for %s)", r.InPerson, square.ScopeInPerson)
	}
	markConnected(s)
	st, err := s.Status(context.Background(), sc.LocationID)
	if err != nil || st.InPerson == nil || !*st.InPerson || len(st.Scopes) == 0 {
		t.Fatalf("status in_person = %v, scopes %v, %v", st.InPerson, st.Scopes, err)
	}
	// Another store: not linked.
	r, err = call(t, s.HandleSession, sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}, map[string]any{})
	if err != nil || r.OK || r.Code != CodeNotLinked || r.AccessToken != "" {
		t.Fatalf("not linked = %+v, %v", r, err)
	}
}

func TestSessionAsksAnOldGrantToReconnect(t *testing.T) {
	s, repo, loc, _, _ := oauthRig(t)
	u, err := s.ConnectURL(loc.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The owner connected before the reader's scope existed.
	pu, _ := url.Parse(u)
	q := pu.Query()
	q.Set("scope", strings.ReplaceAll(q.Get("scope"), square.ScopeInPerson, ""))
	pu.RawQuery = q.Encode()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Get(pu.String())
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	to, _ := url.Parse(res.Header.Get("Location"))
	if _, _, err := s.Finish(context.Background(), to.Query().Get("code"), to.Query().Get("state"), ""); err != nil {
		t.Fatal(err)
	}
	s.repo = connected{repo}
	sc := sseBroker.Scope{TenantID: loc.TenantID, LocationID: loc.ID}
	r, err := call(t, s.HandleSession, sc, map[string]any{})
	if err != nil || r.OK || r.Code != CodeReconnect || r.AccessToken != "" || r.InPerson == nil || *r.InPerson {
		t.Fatalf("old grant = %+v, %v", r, err)
	}
	markConnected(s)
	st, _ := s.Status(context.Background(), loc.ID)
	if st.InPerson == nil || *st.InPerson {
		t.Fatalf("status in_person = %v, want false", st.InPerson)
	}
}

func keyed(nonce, key string, cents, tip int64) map[string]any {
	return map[string]any{"nonce": nonce, "amount_cents": cents, "tip_cents": tip, "idempotency_key": key, "reference_id": "ck-order-1"}
}

func TestKeyedPaymentIsMOTOAndIdempotent(t *testing.T) {
	s, sc, tok := testRefundRig(t)
	r, err := call(t, s.HandleKeyedPayment, sc, keyed("cnon:card-nonce-ok", "att1-k1", 1777, 300))
	if err != nil || !r.OK || r.Payment == nil {
		t.Fatalf("keyed = %+v, %v", r, err)
	}
	p := r.Payment
	if p.ID == "" || p.Status != square.StatusCompleted || p.Last4 != "1111" || p.Brand != "VISA" || p.AuthCode == "" || p.ReceiptNumber == "" || p.Entry != "keyed" {
		t.Fatalf("payment = %+v", p)
	}
	got, err := s.client.GetPayment(context.Background(), tok, p.ID)
	if err != nil || got.AmountMoney.Amount != 1777 || got.TipMoney.Amount != 300 || got.ReferenceID != "ck-order-1" {
		t.Fatalf("at Square: %+v, %v", got, err)
	}
	// The same key again (a lost answer): the same payment, never a second charge.
	again, err := call(t, s.HandleKeyedPayment, sc, keyed("cnon:card-nonce-ok", "att1-k1", 1777, 300))
	if err != nil || !again.OK || again.Payment.ID != p.ID {
		t.Fatalf("again = %+v, %v (first %s)", again, err, p.ID)
	}
}

func TestKeyedPaymentDeclines(t *testing.T) {
	s, sc, _ := testRefundRig(t)
	for nonce, code := range map[string]string{
		"cnon:card-nonce-declined":            "GENERIC_DECLINE",
		"cnon:card-nonce-rejected-cvv":        "CVV_FAILURE",
		"cnon:card-nonce-rejected-postalcode": "ADDRESS_VERIFICATION_FAILURE",
		"cnon:card-nonce-insufficient-funds":  "INSUFFICIENT_FUNDS",
	} {
		r, err := call(t, s.HandleKeyedPayment, sc, keyed(nonce, "k-"+code[:6], 1000, 0))
		if err != nil || r.OK || r.Code != CodeCardDeclined || r.SquareCode != code || r.Error == "" {
			t.Errorf("%s = %+v, %v", nonce, r, err)
		}
	}
}

func TestKeyedPaymentUnknownOutcomeIsAnError(t *testing.T) {
	s, sc, _ := testRefundRig(t)
	fakeFail(t, s, "create", "INTERNAL_SERVER_ERROR", 500)
	if _, err := call(t, s.HandleKeyedPayment, sc, keyed("cnon:card-nonce-ok", "lost-1", 500, 0)); err == nil {
		t.Fatal("a 500 from Square must be an error (the Pi answers 503: unknown)")
	}
	// The same key afterwards goes through once.
	r, err := call(t, s.HandleKeyedPayment, sc, keyed("cnon:card-nonce-ok", "lost-1", 500, 0))
	if err != nil || !r.OK {
		t.Fatalf("retry = %+v, %v", r, err)
	}
}

func TestKeyedPaymentBadBodies(t *testing.T) {
	s, sc, _ := testRefundRig(t)
	for name, b := range map[string]map[string]any{
		"no nonce":   {"amount_cents": 100, "idempotency_key": "k"},
		"zero":       keyed("cnon:card-nonce-ok", "k", 0, 0),
		"neg tip":    keyed("cnon:card-nonce-ok", "k", 100, -1),
		"no key":     keyed("cnon:card-nonce-ok", "", 100, 0),
		"long key":   keyed("cnon:card-nonce-ok", strings.Repeat("k", 46), 100, 0),
		"huge":       keyed("cnon:card-nonce-ok", "k", 1_000_001, 0),
		"huge tip":   keyed("cnon:card-nonce-ok", "k", 100, 100_001),
		"not json":   nil,
		"wrong type": {"amount_cents": "1"},
	} {
		raw, _ := json.Marshal(b)
		if b == nil {
			raw = json.RawMessage(`nope`)
		}
		if _, err := s.HandleKeyedPayment(context.Background(), sc, raw); !errors.Is(err, rpc.ErrBadPayload) {
			t.Errorf("%s: %v, want a bad payload", name, err)
		}
	}
}

func TestRefundThroughSquare(t *testing.T) {
	s, sc, _ := testRefundRig(t)
	r, _ := call(t, s.HandleKeyedPayment, sc, keyed("cnon:card-nonce-ok", "pay-r1", 2000, 300))
	id := r.Payment.ID
	body := map[string]any{"payment_id": id, "amount_cents": 800, "idempotency_key": "ref-1", "reason": "Wrong order"}
	ref, err := call(t, s.HandleRefund, sc, body)
	if err != nil || !ref.OK || ref.RefundID == "" || ref.Status != "PENDING" {
		t.Fatalf("refund = %+v, %v", ref, err)
	}
	again, err := call(t, s.HandleRefund, sc, body)
	if err != nil || !again.OK || again.RefundID != ref.RefundID {
		t.Fatalf("again = %+v, %v", again, err)
	}
	// More than is left (2300 − 800): Square refuses, recorded as refund_failed.
	over, err := call(t, s.HandleRefund, sc, map[string]any{"payment_id": id, "amount_cents": 1501, "idempotency_key": "ref-2"})
	if err != nil || over.OK || over.Code != CodeRefundFailed || over.SquareCode != "REFUND_AMOUNT_INVALID" {
		t.Fatalf("over = %+v, %v", over, err)
	}
	// The rest, then an unknown payment.
	if rest, err := call(t, s.HandleRefund, sc, map[string]any{"payment_id": id, "amount_cents": 1500, "idempotency_key": "ref-3"}); err != nil || !rest.OK {
		t.Fatalf("rest = %+v, %v", rest, err)
	}
	unk, err := call(t, s.HandleRefund, sc, map[string]any{"payment_id": "pay_nope", "amount_cents": 100, "idempotency_key": "ref-4"})
	if err != nil || unk.OK || unk.Code != CodeRefundFailed {
		t.Fatalf("unknown = %+v, %v", unk, err)
	}
	// Square away: an error (not recorded; the manager tries again with the same key).
	fakeFail(t, s, "refund", "SERVICE_UNAVAILABLE", 503)
	r2, _ := call(t, s.HandleKeyedPayment, sc, keyed("cnon:card-nonce-ok", "pay-r2", 1000, 0))
	if _, err := call(t, s.HandleRefund, sc, map[string]any{"payment_id": r2.Payment.ID, "amount_cents": 100, "idempotency_key": "ref-5"}); err == nil {
		t.Fatal("a 503 from Square must be an error")
	}
	// Not linked.
	nl, err := call(t, s.HandleRefund, sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}, map[string]any{"payment_id": id, "amount_cents": 1, "idempotency_key": "k"})
	if err != nil || nl.Code != CodeNotLinked {
		t.Fatalf("not linked = %+v, %v", nl, err)
	}
}

// fakeFail queues one failure for op on the rig's fake Square.
func fakeFail(t *testing.T, s *Service, op, code string, status int) {
	t.Helper()
	body := strings.NewReader(`{"op":"` + op + `","code":"` + code + `","status":` + itoa(status) + `}`)
	res, err := http.Post(s.cfg.BaseURL+"/_fake/fail", "application/json", body)
	if err != nil || res.StatusCode != http.StatusNoContent {
		t.Fatalf("fake fail: %v %v", res, err)
	}
	res.Body.Close()
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// Holds: the rig's store has no card holds (Status counts them).
func (c connected) Holds(context.Context, uuid.UUID, int) ([]repositories.CardHold, error) {
	return nil, nil
}

// markConnected: the rig's repo keeps no status column (the table defaults to connected).
func markConnected(s *Service) {
	for i := range s.repo.(connected).got {
		s.repo.(connected).got[i].Status = "connected"
	}
}
