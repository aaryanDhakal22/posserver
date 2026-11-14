package squarefake

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// The Point of Sale API's side of the fake ("Send to phone", agent contract
// §2.10z): POST /_fake/pos/charge makes a transaction as if a phone had
// charged it in the Square app (a contactless VISA ··1111; the tip is
// tip_cents when the test adds one on "Square's screen"), GET /v2/orders/{id}
// answers it like RetrieveOrder and GET /v2/payments lists them like
// ListPayments. Kept beside the Fake (posBooks) so fake.go stays as it is.

type posPayment struct {
	ID, OrderID, ClientTx, Location, Merchant, Note string
	Total, Tip                                      int64
	Created                                         time.Time
}

type posBook struct {
	mu       sync.Mutex
	byOrder  map[string]*posPayment
	payments []*posPayment
}

var posBooks sync.Map // *Fake -> *posBook

func (f *Fake) pos() *posBook {
	b, _ := posBooks.LoadOrStore(f, &posBook{byOrder: map[string]*posPayment{}})
	return b.(*posBook)
}

// POSCharge makes a transaction for merchant (tests in Go use it directly).
func (f *Fake) POSCharge(merchant, location string, total, tip int64, note string) (orderID, clientTx, paymentID string) {
	b := f.pos()
	p := &posPayment{ID: id("pay_pos_"), OrderID: id("ord_pos_"), ClientTx: id("ctx_"), Location: location, Merchant: merchant,
		Note: note, Total: total, Tip: tip, Created: time.Now().UTC()}
	b.mu.Lock()
	b.byOrder[p.OrderID] = p
	b.payments = append(b.payments, p)
	b.mu.Unlock()
	return p.OrderID, p.ClientTx, p.ID
}

func posCard() map[string]any {
	return map[string]any{"entry_method": "CONTACTLESS", "card": map[string]string{"last_4": "1111", "card_brand": "VISA"}}
}

func (p *posPayment) payment() map[string]any {
	receipt := strings.TrimPrefix(p.ID, "pay_pos_")
	if len(receipt) > 4 {
		receipt = receipt[:4]
	}
	return map[string]any{"id": p.ID, "status": "COMPLETED", "location_id": p.Location, "order_id": p.OrderID, "note": p.Note,
		"created_at": p.Created.Format(time.RFC3339), "receipt_number": receipt, "source_type": "CARD",
		"amount_money": map[string]any{"amount": p.Total, "currency": "USD"}, "tip_money": map[string]any{"amount": p.Tip, "currency": "USD"},
		"total_money": map[string]any{"amount": p.Total + p.Tip, "currency": "USD"}, "card_details": posCard()}
}

func (f *Fake) mountPOS(mux *http.ServeMux, p string) {
	who := func(r *http.Request) (string, bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.merchant(r)
	}
	mux.HandleFunc("POST "+p+"/_fake/pos/charge", func(w http.ResponseWriter, r *http.Request) {
		m, ok := who(r)
		if !ok {
			sqErr(w, http.StatusUnauthorized, "AUTHENTICATION_ERROR", "UNAUTHORIZED", "bad access token")
			return
		}
		var b struct {
			TotalCents int64  `json:"total_cents"`
			TipCents   int64  `json:"tip_cents"`
			Note       string `json:"note"`
			LocationID string `json:"location_id"`
		}
		if !decode(r, &b) || b.TotalCents < 1 {
			sqErr(w, http.StatusBadRequest, "INVALID_REQUEST_ERROR", "BAD_REQUEST", "total_cents")
			return
		}
		order, client, pay := f.POSCharge(m, b.LocationID, b.TotalCents, b.TipCents, b.Note)
		writeJSON(w, http.StatusOK, map[string]any{"transaction_id": order, "client_transaction_id": client, "payment_id": pay})
	})
	mux.HandleFunc("GET "+p+"/v2/orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		m, ok := who(r)
		if !ok {
			sqErr(w, http.StatusUnauthorized, "AUTHENTICATION_ERROR", "UNAUTHORIZED", "bad access token")
			return
		}
		b := f.pos()
		b.mu.Lock()
		pp, found := b.byOrder[r.PathValue("id")]
		b.mu.Unlock()
		if !found || pp.Merchant != m {
			sqErr(w, http.StatusNotFound, "INVALID_REQUEST_ERROR", "NOT_FOUND", "order not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"order": map[string]any{"id": pp.OrderID, "location_id": pp.Location, "state": "COMPLETED",
			"created_at": pp.Created.Format(time.RFC3339), "total_tip_money": map[string]any{"amount": pp.Tip, "currency": "USD"},
			"tenders": []map[string]any{{"id": pp.ID, "payment_id": pp.ID, "type": "CARD",
				"amount_money": map[string]any{"amount": pp.Total + pp.Tip, "currency": "USD"},
				"tip_money":    map[string]any{"amount": pp.Tip, "currency": "USD"}, "card_details": posCard()}}}})
	})
	mux.HandleFunc("GET "+p+"/v2/payments", func(w http.ResponseWriter, r *http.Request) {
		m, ok := who(r)
		if !ok {
			sqErr(w, http.StatusUnauthorized, "AUTHENTICATION_ERROR", "UNAUTHORIZED", "bad access token")
			return
		}
		q := r.URL.Query()
		begin, _ := time.Parse(time.RFC3339, q.Get("begin_time"))
		end, err := time.Parse(time.RFC3339, q.Get("end_time"))
		if err != nil {
			end = time.Now().Add(time.Hour)
		}
		last4 := q.Get("last_4")
		b := f.pos()
		b.mu.Lock()
		out := []map[string]any{}
		for i := len(b.payments) - 1; i >= 0; i-- {
			pp := b.payments[i]
			if pp.Merchant != m || pp.Created.Before(begin.Add(-time.Second)) || pp.Created.After(end) {
				continue
			}
			if loc := q.Get("location_id"); loc != "" && pp.Location != "" && pp.Location != loc {
				continue
			}
			if last4 != "" && last4 != "1111" { // every fake card is a VISA ··1111
				continue
			}
			out = append(out, pp.payment())
		}
		b.mu.Unlock()
		// The card payments CreatePayment made (the reader / manual entry,
		// batch J's general card refund fallback) only when the call filters
		// by a card (last_4): the Point of Sale lookup lists as before.
		if last4 != "" {
			f.mu.Lock()
			if f.failed(w, "list", m) {
				f.mu.Unlock()
				return
			}
			out = append(f.keyedListed(m, q.Get("location_id"), last4, begin, end), out...)
			f.mu.Unlock()
		}
		writeJSON(w, http.StatusOK, map[string]any{"payments": out})
	})
}
