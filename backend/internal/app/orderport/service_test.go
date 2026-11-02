package orderport

import (
	"testing"

	channelDom "quiccpos/main/internal/domain/channel"
	"quiccpos/main/internal/domain/pricing"

	"github.com/google/uuid"
)

func TestNormPhone(t *testing.T) {
	for in, want := range map[string]string{
		"(410) 555-0101": "4105550101", "+1 410 555 0101": "4105550101", "410.555.0101": "4105550101",
		"555-0101": "", "44 20 7946 0958": "", "": "",
	} {
		if got := normPhone(in); got != want {
			t.Errorf("normPhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSourceEventID(t *testing.T) {
	ch := channelDom.Channel{ID: uuid.MustParse("0a1b2c3d-1111-2222-3333-444455556666")}
	if got := sourceEventID(ch, "web-42"); got != "0a1b2c3d-web-42" {
		t.Errorf("sourceEventID = %q", got)
	}
}

func TestValidate(t *testing.T) {
	ok := func() Request {
		r := Request{ClientOrderID: "a-1", Type: "pickup", Lines: []pricing.Line{{Selection: pricing.Selection{ItemID: "x"}}}}
		r.Payment.Method = "cash"
		r.Customer.Name, r.Customer.Phone = " Ada ", "410-555-0101"
		return r
	}
	r := ok()
	if e := r.validate(); e != nil || r.Customer.Name != "Ada" || r.Customer.Phone != "4105550101" {
		t.Fatalf("valid: %v %+v", e, r.Customer)
	}
	for name, mutate := range map[string]func(*Request){
		"client id":        func(r *Request) { r.ClientOrderID = "has space" },
		"type":             func(r *Request) { r.Type = "dinein" },
		"no lines":         func(r *Request) { r.Lines = nil },
		"no name":          func(r *Request) { r.Customer.Name = "" },
		"short phone":      func(r *Request) { r.Customer.Phone = "5550101" },
		"bad email":        func(r *Request) { r.Customer.Email = "nope" },
		"delivery address": func(r *Request) { r.Type = "delivery" },
		"payment":          func(r *Request) { r.Payment.Method = "iou" },
	} {
		r := ok()
		mutate(&r)
		if e := r.validate(); e == nil || e.Code != "bad_request" {
			t.Errorf("%s: %v", name, e)
		}
	}
}
