package square_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"quiccpos/main/internal/infra/square"
	"quiccpos/main/internal/infra/square/squarefake"
)

func setup(t *testing.T) (*square.Client, *squarefake.Fake, string) {
	t.Helper()
	fake := squarefake.New("sq0idp-test", "sq0csp-secret")
	srv := httptest.NewServer(fake.Handler("/", "https://main.example/square/callback"))
	t.Cleanup(srv.Close)
	c := square.New(srv.URL, "sq0idp-test", "sq0csp-secret", srv.Client())
	// Connect: the authorize page redirects back with a code.
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(c.AuthorizeURL("st8", "", false))
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize: %v %v", err, resp)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc.Query().Get("state") != "st8" || loc.Host != "main.example" {
		t.Fatalf("redirect %s", loc)
	}
	tok, err := c.ExchangeCode(context.Background(), loc.Query().Get("code"), "")
	if err != nil || tok.AccessToken == "" || tok.MerchantID == "" || tok.Expires().IsZero() {
		t.Fatalf("exchange: %+v %v", tok, err)
	}
	// A code works once.
	if _, err := c.ExchangeCode(context.Background(), loc.Query().Get("code"), ""); square.StatusOf(err) != http.StatusUnauthorized {
		t.Fatalf("code reused: %v", err)
	}
	return c, fake, tok.AccessToken
}

func TestHoldRaiseCompleteRefund(t *testing.T) {
	c, _, tok := setup(t)
	ctx := context.Background()
	locs, err := c.Locations(ctx, tok)
	if err != nil || len(locs) != 2 || !locs[0].TakesCards() || locs[1].TakesCards() {
		t.Fatalf("locations %+v %v", locs, err)
	}
	p, err := c.CreatePayment(ctx, tok, square.CreatePayment{IdempotencyKey: "k1", SourceID: "cnon:card-nonce-ok",
		AmountMoney: square.USD(2500), TipMoney: square.USD(300), LocationID: locs[0].ID, ReferenceID: "order-1"})
	if err != nil || p.Status != square.StatusApproved || !p.Can(square.CapTipUp) || p.TotalMoney.Amount != 2800 || p.Last4() != "1111" {
		t.Fatalf("create %+v %v", p, err)
	}
	// Same key, same payment.
	if again, _ := c.CreatePayment(ctx, tok, square.CreatePayment{IdempotencyKey: "k1", SourceID: "cnon:card-nonce-ok",
		AmountMoney: square.USD(2500)}); again.ID != p.ID {
		t.Fatal("idempotency")
	}
	if _, err := c.RefundPayment(ctx, tok, "r0", p.ID, square.USD(100), ""); square.CodeOf(err) != "REFUND_ERROR_PAYMENT_NEEDS_COMPLETION" {
		t.Fatalf("refund a hold: %v", err)
	}
	p, err = c.UpdatePayment(ctx, tok, p.ID, "u1", nil, square.USD(800))
	if err != nil || p.TipMoney.Amount != 800 {
		t.Fatalf("raise tip %+v %v", p, err)
	}
	p, err = c.CompletePayment(ctx, tok, p.ID)
	if err != nil || p.Status != square.StatusCompleted || p.TotalMoney.Amount != 3300 {
		t.Fatalf("complete %+v %v", p, err)
	}
	if _, err := c.UpdatePayment(ctx, tok, p.ID, "u2", nil, square.USD(900)); err == nil {
		t.Fatal("updated a completed payment")
	}
	r, err := c.RefundPayment(ctx, tok, "r1", p.ID, square.USD(1000), "test")
	if err != nil || r.AmountMoney.Amount != 1000 {
		t.Fatalf("refund %+v %v", r, err)
	}
	if _, err := c.RefundPayment(ctx, tok, "r2", p.ID, square.USD(5000), ""); square.CodeOf(err) != "REFUND_AMOUNT_INVALID" {
		t.Fatalf("over-refund: %v", err)
	}
}

func TestNoEditSavedCardAndDeclines(t *testing.T) {
	c, _, tok := setup(t)
	ctx := context.Background()
	p, err := c.CreatePayment(ctx, tok, square.CreatePayment{IdempotencyKey: "k2", SourceID: "cnon:card-nonce-no-edit", AmountMoney: square.USD(2000)})
	if err != nil || p.Can(square.CapTipUp) {
		t.Fatalf("create %+v %v", p, err)
	}
	if _, err := c.UpdatePayment(ctx, tok, p.ID, "u", nil, square.USD(500)); square.CodeOf(err) != "AMOUNT_TOO_HIGH" {
		t.Fatalf("raise: %v", err)
	}
	if _, err := c.CompletePayment(ctx, tok, p.ID); err != nil {
		t.Fatal(err)
	}
	cu, err := c.CreateCustomer(ctx, tok, "cu1", square.Customer{GivenName: "Pat", ReferenceID: "order-2"})
	if err != nil || cu.ID == "" {
		t.Fatalf("customer %v", err)
	}
	card, err := c.CreateCard(ctx, tok, "card1", p.ID, cu.ID)
	if err != nil || card.ID == "" {
		t.Fatalf("card %v", err)
	}
	tip, err := c.CreatePayment(ctx, tok, square.CreatePayment{IdempotencyKey: "tip1", SourceID: card.ID, CustomerID: cu.ID,
		AmountMoney: square.USD(500), Autocomplete: true})
	if err != nil || tip.Status != square.StatusCompleted {
		t.Fatalf("tip charge %+v %v", tip, err)
	}
	if err := c.DisableCard(ctx, tok, card.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreatePayment(ctx, tok, square.CreatePayment{IdempotencyKey: "tip2", SourceID: card.ID, CustomerID: cu.ID,
		AmountMoney: square.USD(500), Autocomplete: true}); err == nil {
		t.Fatal("charged a disabled card")
	}

	for src, code := range map[string]string{"cnon:card-nonce-declined": "GENERIC_DECLINE", "cnon:card-nonce-verification": "CARD_DECLINED_VERIFICATION_REQUIRED"} {
		_, err := c.CreatePayment(ctx, tok, square.CreatePayment{IdempotencyKey: "d" + code, SourceID: src, AmountMoney: square.USD(100)})
		if square.CodeOf(err) != code || !square.Declined(err) || square.Temporary(err) {
			t.Fatalf("%s: %v", src, err)
		}
	}
	// Cancel by key voids a hold we lost the answer for.
	if _, err := c.CreatePayment(ctx, tok, square.CreatePayment{IdempotencyKey: "lost", SourceID: "cnon:card-nonce-ok", AmountMoney: square.USD(100)}); err != nil {
		t.Fatal(err)
	}
	if err := c.CancelByIdempotencyKey(ctx, tok, "lost"); err != nil {
		t.Fatal(err)
	}
}

func TestInjectedFailureAndRevoke(t *testing.T) {
	c, fake, tok := setup(t)
	ctx := context.Background()
	fake.FailNext("complete", "TEMPORARY_ERROR", http.StatusServiceUnavailable, 1)
	p, _ := c.CreatePayment(ctx, tok, square.CreatePayment{IdempotencyKey: "k3", SourceID: "cnon:card-nonce-ok", AmountMoney: square.USD(100)})
	if _, err := c.CompletePayment(ctx, tok, p.ID); !square.Temporary(err) {
		t.Fatalf("want a temporary error, got %v", err)
	}
	if _, err := c.CompletePayment(ctx, tok, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.Revoke(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Locations(ctx, tok); square.StatusOf(err) != http.StatusUnauthorized {
		t.Fatalf("revoked token still works: %v", err)
	}
}
