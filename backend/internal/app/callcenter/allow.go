package callcenter

import (
	"encoding/json"
	"net/http"
	"strings"
)

// The relay's allowlist (contract/main.md §2.5zh): the Pi's terminal routes a
// call-center agent may use, checked here and again on the Pi (agent contract
// §2.10ab). Everything else is 403 not_allowed_for_callcenter.

type rule struct {
	method string
	// pattern segments: a literal, or "*" for one path segment (an id).
	pattern []string
}

func r(method, pattern string) rule { return rule{method, strings.Split(pattern, "/")} }

var allowlist = []rule{
	r("GET", "menu"), r("GET", "status"), r("GET", "session"),
	r("POST", "quote"),
	r("POST", "orders"), r("GET", "orders"), r("GET", "orders/*"),
	r("PUT", "orders/*/cart"),
	r("POST", "orders/*/payments"),
	r("POST", "square/keyed-payments"),
	r("POST", "orders/*/void"),
	r("POST", "orders/*/time"),
	r("POST", "orders/*/reprint"),
	r("GET", "pickups"),
	r("GET", "checks"), r("GET", "checks/*"),
	r("GET", "drivers"),
	r("GET", "floor"),
	r("GET", "drafts"), r("PUT", "drafts/*"), r("DELETE", "drafts/*"),
	r("GET", "customers"), r("POST", "customers"),
	r("PATCH", "customers/*"),
	r("POST", "customers/*/addresses"),
	r("GET", "customers/*/orders"),
	r("GET", "addresses"),
	r("POST", "journal"),
}

// CleanPath normalizes a relayed path ("/orders/x/" → "orders/x"); ok is
// false for anything that could leave /api/terminal/ (dots, empty or odd
// segments).
func CleanPath(p string) (string, bool) {
	p = strings.Trim(p, "/")
	if p == "" || len(p) > 300 {
		return "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, "?#%\\ \t\r\n") {
			return "", false
		}
	}
	return p, true
}

// Allowed reports whether method + path (already cleaned) is on the allowlist.
func Allowed(method, path string) bool {
	segs := strings.Split(path, "/")
	for _, a := range allowlist {
		if a.method != method || len(a.pattern) != len(segs) {
			continue
		}
		ok := true
		for i, s := range a.pattern {
			if s != "*" && s != segs[i] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// NeedsOpenStore: the calls refused while the store is closed.
func NeedsOpenStore(method, path string) bool {
	switch {
	case method == http.MethodPost && path == "orders",
		method == http.MethodPost && path == "square/keyed-payments",
		method == http.MethodPut && strings.HasPrefix(path, "orders/") && strings.HasSuffix(path, "/cart"):
		return true
	}
	return false
}

// slowCall: the typed card's charge waits for Square.
func slowCall(path string) bool { return path == "square/keyed-payments" }

// Refusal is a body guard's answer: what the call carried that the call
// center may not do.
type Refusal struct {
	Status int
	Code   string
	What   string
	Error  string
}

func refuse(what, msg string) *Refusal {
	return &Refusal{Status: http.StatusForbidden, Code: CodeNotAllowed, What: what, Error: msg}
}

type guardLine struct {
	Custom     bool   `json:"custom"`
	PriceCents *int64 `json:"price_cents"`
	Comp       bool   `json:"comp"`
}

type guardPayment struct {
	Tender string `json:"tender"`
	Refund bool   `json:"refund"`
}

type guardBody struct {
	Type       *string         `json:"type"`
	ClientKey  string          `json:"client_key"`
	ManagerPin json.RawMessage `json:"manager_pin"`
	Approvals  json.RawMessage `json:"approvals"`
	Refund     bool            `json:"refund"`
	IdemKey    string          `json:"idempotency_key"`
	Tender     *string         `json:"tender"`
	Reason     *string         `json:"reason"`
	Payments   []guardPayment  `json:"payments"`
	Cart       *struct {
		Discount     json.RawMessage `json:"discount"`
		EmployeeMeal json.RawMessage `json:"employee_meal"`
		Lines        []guardLine     `json:"lines"`
	} `json:"cart"`
}

func present(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != `""` && s != "[]" && s != "false"
}

// Guard checks an allowed call's body for what the call center may not carry
// (contract §2.5zh "What an allowed call may not carry"). main's check is the
// first line; the Pi's guards for the device kind are the authority (they know
// the stored order: a custom line it already holds rides along on PUT cart, so
// main lets custom lines through there).
func Guard(method, path string, body []byte) *Refusal {
	if len(body) == 0 || method == http.MethodGet || method == http.MethodDelete {
		return nil
	}
	var b guardBody
	if err := json.Unmarshal(body, &b); err != nil {
		// Fail closed: a body the guards can't read (not an object, or a
		// field of the wrong JSON type: {"tender": "comp", "client_key": 7})
		// never reaches the Pi.
		return &Refusal{Status: http.StatusBadRequest, Code: "bad_request", Error: "The body isn't what this call takes."}
	}
	if present(b.ManagerPin) || present(b.Approvals) {
		return refuse("manager", "The call center can't use a manager's PIN or approval.")
	}
	if b.Refund {
		return refuse("refund", "Refunds are done at the store.")
	}
	tenders := []string{}
	if b.Tender != nil && strings.HasSuffix(path, "/payments") {
		tenders = append(tenders, *b.Tender)
	}
	for _, p := range b.Payments {
		if p.Refund {
			return refuse("refund", "Refunds are done at the store.")
		}
		tenders = append(tenders, p.Tender)
	}
	for _, t := range tenders {
		if t == "comp" {
			return refuse("comp", "The call center can't comp an order.")
		}
		if t != "manual_card" {
			return refuse("tender", "The call center takes a typed card only; cash is collected at the store or the door.")
		}
	}
	if path == "orders" && method == http.MethodPost {
		if b.Type == nil || (*b.Type != "pickup" && *b.Type != "delivery") {
			return refuse("type", "The call center places pickup and delivery orders only.")
		}
		if strings.TrimSpace(b.ClientKey) == "" {
			return &Refusal{Status: http.StatusBadRequest, Code: "client_key_required", Error: "client_key is required"}
		}
	}
	// A retry must be safe by construction (a reply can be lost between the
	// Pi and main): no charge without its key.
	if path == "square/keyed-payments" && strings.TrimSpace(b.IdemKey) == "" {
		return &Refusal{Status: http.StatusBadRequest, Code: "idempotency_key_required", Error: "idempotency_key is required"}
	}
	if b.Type != nil && *b.Type == "counter" {
		return refuse("type", "The call center places pickup and delivery orders only.")
	}
	if b.Cart != nil {
		if present(b.Cart.Discount) {
			return refuse("discount", "The call center can't give discounts (coupons only).")
		}
		if present(b.Cart.EmployeeMeal) {
			return refuse("discount", "The call center can't give discounts (coupons only).")
		}
		// Lines: only when placing. A quote changes nothing, and a cart change
		// may carry lines the stored order already holds (a cashier's Quicc
		// Add, a kept price): the Pi knows which are new.
		if path == "orders" && method == http.MethodPost {
			for _, l := range b.Cart.Lines {
				switch {
				case l.Comp:
					return refuse("comp", "The call center can't comp a line.")
				case l.Custom:
					// Quick add (owner 2026-11-13): the call center types a name +
					// price like the terminal's Quicc Add; the Pi checks it and
					// prints it where the store's Quick add item routes.
				case l.PriceCents != nil:
					return refuse("price_change", "The call center can't change a price.")
				}
			}
		}
	}
	return nil
}
