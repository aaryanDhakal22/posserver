// Package squarefake is an in-memory stand-in for the parts of Square's API
// main uses (package square): OAuth, locations, payments (holds, updates,
// complete, cancel, cancel by key), refunds, customers and cards. Go tests
// run it with httptest; dev (and staging until the owner's Square app
// exists) mounts it inside main under /__fake/square so pytest can drive the
// whole card path. It is never mounted in prod.
//
// Card tokens, like Square's sandbox:
//
//	cnon:card-nonce-ok                  approved; the hold may be raised (tip + amount)
//	cnon:card-nonce-no-edit             approved; the hold can't be raised (like most online holds)
//	cnon:card-edit-tip-failure          (Square's sandbox name for the same)
//	cnon:card-nonce-declined            400 GENERIC_DECLINE
//	cnon:card-nonce-rejected-cvv        400 CVV_FAILURE
//	cnon:card-nonce-verification        402 CARD_DECLINED_VERIFICATION_REQUIRED
//	cnon:card-nonce-slow-<seconds>      approved like no-edit, after <seconds> (≤ 60; tests of slow placements)
//	ccof:…                              a saved card (charged at once)
//
// Test hooks (no auth): GET _fake/payments/{id}, GET _fake/state,
// POST _fake/fail {op, code, status, count} makes the next count calls of op
// (create|update|complete|cancel|refund|card|customer|token) fail, POST
// _fake/expire {payment_id} plays Square's 7-day delay action (cancel).
package squarefake

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Fake is the in-memory Square.
type Fake struct {
	mu        sync.Mutex
	AppID     string
	AppSecret string
	codes     map[string]bool             // issued OAuth codes
	access    map[string]string           // access token → merchant
	refresh   map[string]string           // refresh token → merchant
	payments  map[string]*payment         // id → payment
	byKey     map[string]string           // idempotency key → payment / refund / customer / card id
	refunds   map[string]map[string]any   // id → refund
	customers map[string]map[string]any   // id → customer
	cards     map[string]map[string]any   // id → card
	fail      map[string][]failure        // op → queued failures
	calls     map[string]int              // op → calls (tests read it)
	revoked   map[string]bool             // merchant → revoked
	locations map[string][]map[string]any // merchant → locations
}

type failure struct {
	Code   string
	Status int
}

type payment struct {
	ID, Status, Location, Customer, Reference, Source string
	Amount, Tip, Refunded                             int64
	CanEdit                                           bool
	Created                                           time.Time
	Version                                           int
	Card                                              string // ccof id when charged on a saved card
}

func New(appID, appSecret string) *Fake {
	return &Fake{AppID: appID, AppSecret: appSecret, codes: map[string]bool{}, access: map[string]string{}, refresh: map[string]string{},
		payments: map[string]*payment{}, byKey: map[string]string{}, refunds: map[string]map[string]any{},
		customers: map[string]map[string]any{}, cards: map[string]map[string]any{}, fail: map[string][]failure{},
		calls: map[string]int{}, revoked: map[string]bool{}, locations: map[string][]map[string]any{}}
}

func id(prefix string) string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func sqErr(w http.ResponseWriter, status int, category, code, detail string) {
	writeJSON(w, status, map[string]any{"errors": []map[string]string{{"category": category, "code": code, "detail": detail}}})
}

// Calls is how many times op was called.
func (f *Fake) Calls(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[op]
}

// FailNext makes the next count calls of op fail with code/status.
func (f *Fake) FailNext(op, code string, status, count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := 0; i < count; i++ {
		f.fail[op] = append(f.fail[op], failure{Code: code, Status: status})
	}
}

// Payment is a payment's state as JSON-able fields (tests).
func (f *Fake) Payment(id string) (map[string]any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.payments[id]
	if !ok {
		return nil, false
	}
	return f.view(p), true
}

// failed pops a queued failure for op (callers hold mu).
func (f *Fake) failed(w http.ResponseWriter, op string) bool {
	f.calls[op]++
	q := f.fail[op]
	if len(q) == 0 {
		return false
	}
	f.fail[op] = q[1:]
	cat := "API_ERROR"
	if q[0].Status == http.StatusBadRequest || q[0].Status == http.StatusPaymentRequired {
		cat = "PAYMENT_METHOD_ERROR"
	}
	sqErr(w, q[0].Status, cat, q[0].Code, "injected failure")
	return true
}

func (f *Fake) merchant(r *http.Request) (string, bool) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	m, ok := f.access[tok]
	if !ok || f.revoked[m] {
		return "", false
	}
	return m, true
}

func (f *Fake) view(p *payment) map[string]any {
	caps := []string{"EDIT_DELAY_ACTION"}
	if p.CanEdit && p.Status == "APPROVED" {
		caps = append(caps, "EDIT_AMOUNT_UP", "EDIT_AMOUNT_DOWN", "EDIT_TIP_AMOUNT_UP", "EDIT_TIP_AMOUNT_DOWN")
	}
	if p.Status != "APPROVED" {
		caps = nil
	}
	v := map[string]any{
		"id": p.ID, "status": p.Status, "location_id": p.Location, "reference_id": p.Reference, "customer_id": p.Customer,
		"amount_money":   map[string]any{"amount": p.Amount, "currency": "USD"},
		"tip_money":      map[string]any{"amount": p.Tip, "currency": "USD"},
		"total_money":    map[string]any{"amount": p.Amount + p.Tip, "currency": "USD"},
		"refunded_money": map[string]any{"amount": p.Refunded, "currency": "USD"},
		"capabilities":   caps, "version_token": "v" + string(rune('0'+p.Version%10)),
		"delayed_until": p.Created.Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339),
		"card_details": map[string]any{"status": map[string]string{"APPROVED": "AUTHORIZED", "COMPLETED": "CAPTURED", "CANCELED": "VOIDED"}[p.Status],
			"auth_result_code": "FAKE01", "card": map[string]string{"last_4": "1111", "card_brand": "VISA"}},
	}
	if p.Card != "" {
		v["source_card_id"] = p.Card
	}
	return v
}

func decode(r *http.Request, v any) bool { return json.NewDecoder(r.Body).Decode(v) == nil }

// Handler serves the fake under prefix (e.g. "/__fake/square"); redirect is
// where the OAuth page sends the owner back when the request names none.
func (f *Fake) Handler(prefix, redirect string) http.Handler {
	mux := http.NewServeMux()
	p := strings.TrimRight(prefix, "/")

	// --- OAuth ---
	mux.HandleFunc("GET "+p+"/oauth2/authorize", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("client_id") != f.AppID {
			http.Error(w, "unknown client_id", http.StatusBadRequest)
			return
		}
		to := r.URL.Query().Get("redirect_uri")
		if to == "" {
			to = redirect
		}
		code := id("sq0cgp-")
		f.mu.Lock()
		f.codes[code] = true
		f.mu.Unlock()
		u, err := url.Parse(to)
		if err != nil || to == "" {
			http.Error(w, "no redirect", http.StatusBadRequest)
			return
		}
		q := u.Query()
		if r.URL.Query().Get("deny") != "" {
			q.Set("error", "access_denied")
		} else {
			q.Set("code", code)
		}
		q.Set("state", r.URL.Query().Get("state"))
		u.RawQuery = q.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
	mux.HandleFunc("POST "+p+"/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failed(w, "token") {
			return
		}
		if !decode(r, &b) || b["client_id"] != f.AppID || b["client_secret"] != f.AppSecret {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"type": "unauthorized", "message": "bad client"})
			return
		}
		var merchant string
		switch b["grant_type"] {
		case "authorization_code":
			if !f.codes[b["code"]] {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"type": "unauthorized", "message": "bad code"})
				return
			}
			delete(f.codes, b["code"])
			merchant = id("ML")
			f.locations[merchant] = []map[string]any{
				{"id": id("L"), "name": "Main store", "status": "ACTIVE", "currency": "USD", "capabilities": []string{"CREDIT_CARD_PROCESSING", "AUTOMATIC_TRANSFERS"}},
				{"id": id("L"), "name": "Old store", "status": "INACTIVE", "currency": "USD", "capabilities": []string{}},
			}
		case "refresh_token":
			m, ok := f.refresh[b["refresh_token"]]
			if !ok || f.revoked[m] {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"type": "unauthorized", "message": "bad refresh token"})
				return
			}
			merchant = m
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"type": "bad_request", "message": "grant_type"})
			return
		}
		at, rt := id("EAAA"), b["refresh_token"]
		if rt == "" {
			rt = id("EQAA")
			f.refresh[rt] = merchant
		}
		f.access[at] = merchant
		writeJSON(w, http.StatusOK, map[string]any{"access_token": at, "refresh_token": rt, "merchant_id": merchant,
			"token_type": "bearer", "expires_at": time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)})
	})
	mux.HandleFunc("POST "+p+"/oauth2/revoke", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Client "+f.AppSecret || !decode(r, &b) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"type": "unauthorized", "message": "bad client"})
			return
		}
		if m, ok := f.access[b["access_token"]]; ok {
			f.revoked[m] = true
		}
		writeJSON(w, http.StatusOK, map[string]bool{"success": true})
	})

	// --- authenticated API ---
	authed := func(op string, h func(w http.ResponseWriter, r *http.Request, merchant string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			defer f.mu.Unlock()
			m, ok := f.merchant(r)
			if !ok {
				sqErr(w, http.StatusUnauthorized, "AUTHENTICATION_ERROR", "UNAUTHORIZED", "bad access token")
				return
			}
			if op != "" && f.failed(w, op) {
				return
			}
			h(w, r, m)
		}
	}
	mux.HandleFunc("GET "+p+"/v2/locations", authed("", func(w http.ResponseWriter, r *http.Request, m string) {
		writeJSON(w, http.StatusOK, map[string]any{"locations": f.locations[m]})
	}))
	createPayment := authed("create", func(w http.ResponseWriter, r *http.Request, m string) {
		var b struct {
			IdempotencyKey string `json:"idempotency_key"`
			SourceID       string `json:"source_id"`
			Amount         *struct {
				Amount int64 `json:"amount"`
			} `json:"amount_money"`
			Tip *struct {
				Amount int64 `json:"amount"`
			} `json:"tip_money"`
			Autocomplete *bool  `json:"autocomplete"`
			LocationID   string `json:"location_id"`
			CustomerID   string `json:"customer_id"`
			ReferenceID  string `json:"reference_id"`
		}
		if !decode(r, &b) || b.IdempotencyKey == "" || b.Amount == nil || b.Amount.Amount <= 0 {
			sqErr(w, http.StatusBadRequest, "INVALID_REQUEST_ERROR", "BAD_REQUEST", "idempotency_key, source_id and amount_money are required")
			return
		}
		if pid, ok := f.byKey["pay:"+b.IdempotencyKey]; ok {
			writeJSON(w, http.StatusOK, map[string]any{"payment": f.view(f.payments[pid])})
			return
		}
		canEdit := false
		card := ""
		switch {
		case b.SourceID == "cnon:card-nonce-ok":
			canEdit = true
		case b.SourceID == "cnon:card-nonce-no-edit", b.SourceID == "cnon:card-edit-tip-failure", b.SourceID == "cnon:card-edit-failure",
			strings.HasPrefix(b.SourceID, "cnon:card-nonce-slow-"):
		case b.SourceID == "cnon:card-nonce-declined":
			sqErr(w, http.StatusBadRequest, "PAYMENT_METHOD_ERROR", "GENERIC_DECLINE", "Authorization error: 'GENERIC_DECLINE'")
			return
		case b.SourceID == "cnon:card-nonce-rejected-cvv":
			sqErr(w, http.StatusBadRequest, "PAYMENT_METHOD_ERROR", "CVV_FAILURE", "Authorization error: 'CVV_FAILURE'")
			return
		case b.SourceID == "cnon:card-nonce-verification":
			sqErr(w, http.StatusPaymentRequired, "PAYMENT_METHOD_ERROR", "CARD_DECLINED_VERIFICATION_REQUIRED", "verification required")
			return
		case strings.HasPrefix(b.SourceID, "ccof:"):
			c, ok := f.cards[b.SourceID]
			if !ok || c["enabled"] != true || c["customer_id"] != b.CustomerID {
				sqErr(w, http.StatusBadRequest, "INVALID_REQUEST_ERROR", "INVALID_CARD", "card not usable")
				return
			}
			card = b.SourceID
			canEdit = c["source"] == "cnon:card-nonce-ok"
		default:
			sqErr(w, http.StatusBadRequest, "INVALID_REQUEST_ERROR", "INVALID_CARD_DATA", "unknown source_id")
			return
		}
		loc := b.LocationID
		if loc == "" && len(f.locations[m]) > 0 {
			loc, _ = f.locations[m][0]["id"].(string)
		}
		pay := &payment{ID: id("pay_"), Status: "APPROVED", Location: loc, Customer: b.CustomerID, Reference: b.ReferenceID,
			Source: b.SourceID, Amount: b.Amount.Amount, CanEdit: canEdit, Created: time.Now(), Card: card}
		if b.Tip != nil {
			pay.Tip = b.Tip.Amount
		}
		if b.Autocomplete == nil || *b.Autocomplete {
			pay.Status = "COMPLETED"
		}
		f.payments[pay.ID] = pay
		f.byKey["pay:"+b.IdempotencyKey] = pay.ID
		writeJSON(w, http.StatusOK, map[string]any{"payment": f.view(pay)})
	})
	// A slow card (cnon:card-nonce-slow-<s>) waits before Square answers, outside the lock.
	mux.HandleFunc("POST "+p+"/v2/payments", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if i := bytes.Index(body, []byte(`"cnon:card-nonce-slow-`)); i >= 0 {
			rest := body[i+len(`"cnon:card-nonce-slow-`):]
			if j := bytes.IndexByte(rest, '"'); j > 0 {
				if n, err := strconv.Atoi(string(rest[:j])); err == nil && n > 0 && n <= 60 {
					time.Sleep(time.Duration(n) * time.Second)
				}
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		createPayment(w, r)
	})
	mux.HandleFunc("POST "+p+"/v2/payments/cancel", authed("cancel", func(w http.ResponseWriter, r *http.Request, m string) {
		var b map[string]string
		if !decode(r, &b) || b["idempotency_key"] == "" {
			sqErr(w, http.StatusBadRequest, "INVALID_REQUEST_ERROR", "BAD_REQUEST", "idempotency_key")
			return
		}
		if pid, ok := f.byKey["pay:"+b["idempotency_key"]]; ok && f.payments[pid].Status == "APPROVED" {
			f.payments[pid].Status = "CANCELED"
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	}))
	mux.HandleFunc("GET "+p+"/v2/payments/{id}", authed("", func(w http.ResponseWriter, r *http.Request, m string) {
		pay, ok := f.payments[r.PathValue("id")]
		if !ok {
			sqErr(w, http.StatusNotFound, "INVALID_REQUEST_ERROR", "NOT_FOUND", "no payment")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"payment": f.view(pay)})
	}))
	mux.HandleFunc("PUT "+p+"/v2/payments/{id}", authed("update", func(w http.ResponseWriter, r *http.Request, m string) {
		pay, ok := f.payments[r.PathValue("id")]
		if !ok {
			sqErr(w, http.StatusNotFound, "INVALID_REQUEST_ERROR", "NOT_FOUND", "no payment")
			return
		}
		var b struct {
			IdempotencyKey string `json:"idempotency_key"`
			Payment        struct {
				Amount *struct {
					Amount int64 `json:"amount"`
				} `json:"amount_money"`
				Tip *struct {
					Amount int64 `json:"amount"`
				} `json:"tip_money"`
			} `json:"payment"`
		}
		if !decode(r, &b) || b.IdempotencyKey == "" {
			sqErr(w, http.StatusBadRequest, "INVALID_REQUEST_ERROR", "BAD_REQUEST", "idempotency_key")
			return
		}
		if done, ok := f.byKey["upd:"+b.IdempotencyKey]; ok && done == pay.ID {
			writeJSON(w, http.StatusOK, map[string]any{"payment": f.view(pay)})
			return
		}
		if pay.Status != "APPROVED" {
			sqErr(w, http.StatusBadRequest, "INVALID_REQUEST_ERROR", "BAD_REQUEST", "only APPROVED payments can be updated")
			return
		}
		up := (b.Payment.Amount != nil && b.Payment.Amount.Amount > pay.Amount) || (b.Payment.Tip != nil && b.Payment.Tip.Amount > pay.Tip)
		if up && !pay.CanEdit {
			sqErr(w, http.StatusBadRequest, "PAYMENT_METHOD_ERROR", "AMOUNT_TOO_HIGH", "EDIT_TIP_AMOUNT_UP capability is not available")
			return
		}
		if b.Payment.Amount != nil {
			pay.Amount = b.Payment.Amount.Amount
		}
		if b.Payment.Tip != nil {
			pay.Tip = b.Payment.Tip.Amount
		}
		pay.Version++
		f.byKey["upd:"+b.IdempotencyKey] = pay.ID
		writeJSON(w, http.StatusOK, map[string]any{"payment": f.view(pay)})
	}))
	mux.HandleFunc("POST "+p+"/v2/payments/{id}/complete", authed("complete", func(w http.ResponseWriter, r *http.Request, m string) {
		pay, ok := f.payments[r.PathValue("id")]
		if !ok {
			sqErr(w, http.StatusNotFound, "INVALID_REQUEST_ERROR", "NOT_FOUND", "no payment")
			return
		}
		switch pay.Status {
		case "APPROVED":
			pay.Status = "COMPLETED"
			pay.Version++
		case "COMPLETED":
		default:
			sqErr(w, http.StatusBadRequest, "INVALID_REQUEST_ERROR", "BAD_REQUEST", "payment is "+pay.Status)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"payment": f.view(pay)})
	}))
	mux.HandleFunc("POST "+p+"/v2/payments/{id}/cancel", authed("cancel", func(w http.ResponseWriter, r *http.Request, m string) {
		pay, ok := f.payments[r.PathValue("id")]
		if !ok {
			sqErr(w, http.StatusNotFound, "INVALID_REQUEST_ERROR", "NOT_FOUND", "no payment")
			return
		}
		switch pay.Status {
		case "APPROVED":
			pay.Status = "CANCELED"
			pay.Version++
		case "CANCELED":
		default:
			sqErr(w, http.StatusBadRequest, "INVALID_REQUEST_ERROR", "BAD_REQUEST", "payment is "+pay.Status)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"payment": f.view(pay)})
	}))
	mux.HandleFunc("POST "+p+"/v2/refunds", authed("refund", func(w http.ResponseWriter, r *http.Request, m string) {
		var b struct {
			IdempotencyKey string `json:"idempotency_key"`
			PaymentID      string `json:"payment_id"`
			Amount         *struct {
				Amount int64 `json:"amount"`
			} `json:"amount_money"`
		}
		if !decode(r, &b) || b.IdempotencyKey == "" || b.Amount == nil {
			sqErr(w, http.StatusBadRequest, "INVALID_REQUEST_ERROR", "BAD_REQUEST", "idempotency_key, payment_id, amount_money")
			return
		}
		if rid, ok := f.byKey["ref:"+b.IdempotencyKey]; ok {
			writeJSON(w, http.StatusOK, map[string]any{"refund": f.refunds[rid]})
			return
		}
		pay, ok := f.payments[b.PaymentID]
		switch {
		case !ok:
			sqErr(w, http.StatusNotFound, "INVALID_REQUEST_ERROR", "NOT_FOUND", "no payment")
			return
		case pay.Status == "APPROVED":
			sqErr(w, http.StatusBadRequest, "INVALID_REQUEST_ERROR", "REFUND_ERROR_PAYMENT_NEEDS_COMPLETION", "complete or cancel it")
			return
		case pay.Status != "COMPLETED" || b.Amount.Amount <= 0 || b.Amount.Amount > pay.Amount+pay.Tip-pay.Refunded:
			sqErr(w, http.StatusBadRequest, "INVALID_REQUEST_ERROR", "REFUND_AMOUNT_INVALID", "bad refund amount")
			return
		}
		pay.Refunded += b.Amount.Amount
		ref := map[string]any{"id": id("ref_"), "status": "PENDING", "payment_id": pay.ID,
			"amount_money": map[string]any{"amount": b.Amount.Amount, "currency": "USD"}}
		f.refunds[ref["id"].(string)] = ref
		f.byKey["ref:"+b.IdempotencyKey] = ref["id"].(string)
		writeJSON(w, http.StatusOK, map[string]any{"refund": ref})
	}))
	mux.HandleFunc("POST "+p+"/v2/customers", authed("customer", func(w http.ResponseWriter, r *http.Request, m string) {
		var b map[string]any
		if !decode(r, &b) {
			sqErr(w, http.StatusBadRequest, "INVALID_REQUEST_ERROR", "BAD_REQUEST", "body")
			return
		}
		key, _ := b["idempotency_key"].(string)
		if cid, ok := f.byKey["cus:"+key]; ok && key != "" {
			writeJSON(w, http.StatusOK, map[string]any{"customer": f.customers[cid]})
			return
		}
		delete(b, "idempotency_key")
		b["id"] = id("CUS")
		f.customers[b["id"].(string)] = b
		if key != "" {
			f.byKey["cus:"+key] = b["id"].(string)
		}
		writeJSON(w, http.StatusOK, map[string]any{"customer": b})
	}))
	mux.HandleFunc("POST "+p+"/v2/cards", authed("card", func(w http.ResponseWriter, r *http.Request, m string) {
		var b struct {
			IdempotencyKey string            `json:"idempotency_key"`
			SourceID       string            `json:"source_id"`
			Card           map[string]string `json:"card"`
		}
		if !decode(r, &b) || b.IdempotencyKey == "" {
			sqErr(w, http.StatusBadRequest, "INVALID_REQUEST_ERROR", "BAD_REQUEST", "idempotency_key")
			return
		}
		if cid, ok := f.byKey["card:"+b.IdempotencyKey]; ok {
			writeJSON(w, http.StatusOK, map[string]any{"card": f.cards[cid]})
			return
		}
		if _, ok := f.customers[b.Card["customer_id"]]; !ok {
			sqErr(w, http.StatusBadRequest, "INVALID_REQUEST_ERROR", "NOT_FOUND", "no customer")
			return
		}
		source := "cnon:card-nonce-no-edit"
		switch {
		case strings.HasPrefix(b.SourceID, "cnon:"):
			// A card token: the same outcomes as paying with it.
			switch b.SourceID {
			case "cnon:card-nonce-ok", "cnon:card-nonce-no-edit", "cnon:card-edit-tip-failure", "cnon:card-edit-failure":
				source = b.SourceID
			case "cnon:card-nonce-declined":
				sqErr(w, http.StatusBadRequest, "PAYMENT_METHOD_ERROR", "GENERIC_DECLINE", "Authorization error: 'GENERIC_DECLINE'")
				return
			case "cnon:card-nonce-verification":
				sqErr(w, http.StatusPaymentRequired, "PAYMENT_METHOD_ERROR", "CARD_DECLINED_VERIFICATION_REQUIRED", "verification required")
				return
			default:
				sqErr(w, http.StatusBadRequest, "INVALID_REQUEST_ERROR", "INVALID_CARD_DATA", "unknown source_id")
				return
			}
		default:
			if pay, ok := f.payments[b.SourceID]; !ok || pay.Status != "COMPLETED" {
				sqErr(w, http.StatusBadRequest, "INVALID_REQUEST_ERROR", "INVALID_CARD", "source must be a completed payment or a card token")
				return
			}
		}
		c := map[string]any{"id": id("ccof:"), "customer_id": b.Card["customer_id"], "last_4": "1111", "card_brand": "VISA", "enabled": true,
			"exp_month": 12, "exp_year": 2030, "source": source}
		f.cards[c["id"].(string)] = c
		f.byKey["card:"+b.IdempotencyKey] = c["id"].(string)
		writeJSON(w, http.StatusOK, map[string]any{"card": c})
	}))
	mux.HandleFunc("POST "+p+"/v2/cards/{id}/disable", authed("", func(w http.ResponseWriter, r *http.Request, m string) {
		c, ok := f.cards[r.PathValue("id")]
		if !ok {
			sqErr(w, http.StatusNotFound, "INVALID_REQUEST_ERROR", "NOT_FOUND", "no card")
			return
		}
		c["enabled"] = false
		writeJSON(w, http.StatusOK, map[string]any{"card": c})
	}))

	// --- test hooks ---
	mux.HandleFunc("GET "+p+"/_fake/payments/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, ok := f.Payment(r.PathValue("id"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
	mux.HandleFunc("GET "+p+"/_fake/cards/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		c, ok := f.cards[r.PathValue("id")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, c)
	})
	mux.HandleFunc("GET "+p+"/_fake/payments", func(w http.ResponseWriter, r *http.Request) {
		// ?reference_id= : the payments made for one order (tests).
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []map[string]any{}
		for _, pay := range f.payments {
			if ref := r.URL.Query().Get("reference_id"); ref == "" || pay.Reference == ref {
				out = append(out, f.view(pay))
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"payments": out})
	})
	mux.HandleFunc("POST "+p+"/_fake/fail", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Op, Code      string
			Status, Count int
		}
		if !decode(r, &b) || b.Op == "" {
			http.Error(w, "op", http.StatusBadRequest)
			return
		}
		if b.Count <= 0 {
			b.Count = 1
		}
		if b.Status == 0 {
			b.Status = http.StatusServiceUnavailable
		}
		if b.Code == "" {
			b.Code = "TEMPORARY_ERROR"
		}
		f.FailNext(b.Op, b.Code, b.Status, b.Count)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST "+p+"/_fake/expire", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		f.mu.Lock()
		defer f.mu.Unlock()
		if !decode(r, &b) {
			http.Error(w, "body", http.StatusBadRequest)
			return
		}
		if pay, ok := f.payments[b["payment_id"]]; ok && pay.Status == "APPROVED" {
			pay.Status = "CANCELED"
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}
