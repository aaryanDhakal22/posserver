package order

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Printer commands a name must never carry to the kitchen.
const (
	escDrawer = "\x1bp\x00\x19\xfa"    // ESC p 0 25 250: kick the cash drawer
	dleDrawer = "\x10\x14\x01\x00\x01" // DLE DC4 1 0 1: real-time drawer pulse
	gsCut     = "\x1dV\x00"            // GS V 0: cut the paper
	escInit   = "\x1b@"                // ESC @: reset the printer
	escBig    = "\x1b!\x30"            // ESC ! 0x30: double size
)

func dirtyInput(src Source) NewOrderInput {
	mod := uuid.Must(uuid.NewV7())
	return NewOrderInput{
		TenantID: TenantID(uuid.New()), LocationID: LocationID(uuid.New()), Source: src, SourceEventID: "evt-1",
		Customer: Customer{DisplayName: "Bob" + escDrawer + " Smith", Phone: "410\x1b555\n1212", Email: "bob@example.com\x00"},
		Fulfillment: Fulfillment{Type: FulfillmentDelivery, Address: &Address{
			Line1: "931 Taylor Ave" + gsCut, Line2: "Apt\t4" + dleDrawer, City: "Towson" + escInit, State: "MD\x7f", PostalCode: "21286\r\n",
			Notes: "ring bell\nPAID - CARD" + escDrawer,
		}},
		Items: []LineItem{{
			ID: uuid.Must(uuid.NewV7()), Name: "Cheese Pizza" + gsCut, Size: "Large" + escBig, Quantity: 1, UnitPrice: 1000, LineTotal: 1000,
			Notes: "well done\n\nFREE - MANAGER" + dleDrawer, SKU: "CP\x1b1", Coupon: "2 for 1" + escInit,
			Children: []LineItem{{ID: mod, Name: "Extra cheese" + escDrawer, Quantity: 1, Notes: "light" + gsCut}},
		}},
		Money: MoneyBreakdown{Subtotal: 1000, Total: 1000,
			Fees:      []Fee{{Type: "delivery", Label: "Delivery" + gsCut, Amount: 0}},
			Discounts: []Discount{{Type: "promo", Code: "SAVE" + escInit, Label: "Save" + dleDrawer, Amount: 0}}},
		Currency: "USD", PlacedAt: time.Now(), Notes: "no onions" + escDrawer + "\nCASH PAID" + gsCut,
		SourceMetadata: map[string]any{"taken_by": "Sam" + escInit, "terminal": map[string]any{"cart": []any{map[string]any{"name": "Pie" + gsCut}}}},
	}
}

func assertNoControl(t *testing.T, what, s string) {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Errorf("%s is not valid UTF-8: %q", what, s)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Errorf("%s keeps control character %#x: %q", what, r, s)
			return
		}
	}
}

// H3: whatever door an order comes through, NewOrder strips control
// characters from every free-text field, so no ESC p / DLE DC4 / GS V / ESC @
// reaches the stored order, the SSE frame or a printer, and a line break in
// a note can't forge a line.
func TestNewOrderStripsControlCharacters(t *testing.T) {
	for _, src := range []Source{SourceOnline, SourceCounter, Source("web")} {
		in := dirtyInput(src)
		in.MoneyFlagged = true
		o, err := NewOrder(in)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		c, f, it := o.Customer(), o.Fulfillment(), o.Items()[0]
		texts := map[string]string{
			"customer name": c.DisplayName, "phone": c.Phone, "email": c.Email,
			"line1": f.Address.Line1, "line2": f.Address.Line2, "city": f.Address.City, "state": f.Address.State,
			"zip": f.Address.PostalCode, "address notes": f.Address.Notes,
			"item name": it.Name, "item size": it.Size, "item notes": it.Notes, "sku": it.SKU, "coupon": it.Coupon,
			"modifier name": it.Children[0].Name, "modifier notes": it.Children[0].Notes,
			"order notes": o.Notes(), "fee label": o.Money().Fees[0].Label,
			"discount code": o.Money().Discounts[0].Code, "discount label": o.Money().Discounts[0].Label,
			"metadata taken_by":  o.SourceMetadata()["taken_by"].(string),
			"metadata cart name": o.SourceMetadata()["terminal"].(map[string]any)["cart"].([]any)[0].(map[string]any)["name"].(string),
		}
		for what, s := range texts {
			assertNoControl(t, string(src)+" "+what, s)
		}
		if c.DisplayName != "Bobp Smith" || f.Address.Line1 != "931 Taylor AveV" || it.Name != "Cheese PizzaV" || it.Children[0].Name != "Extra cheesep" {
			t.Errorf("%s: names = %q / %q / %q / %q", src, c.DisplayName, f.Address.Line1, it.Name, it.Children[0].Name)
		}
		// The forged lines are one line now.
		if o.Notes() != "no onionsp CASH PAIDV" || f.Address.Notes != "ring bell PAID - CARDp" || it.Notes != "well done  FREE - MANAGER" {
			t.Errorf("%s: notes = %q / %q / %q", src, o.Notes(), f.Address.Notes, it.Notes)
		}
		// The caller's values are not rewritten under it.
		if in.Items[0].Name != "Cheese Pizza"+gsCut || in.Fulfillment.Address.Line1 != "931 Taylor Ave"+gsCut || in.SourceMetadata["taken_by"] != "Sam"+escInit {
			t.Errorf("%s: NewOrder changed its caller's input", src)
		}
		// What goes out on the wire (the order.received event) is the clean copy.
		evs := o.PullEvents()
		if len(evs) == 0 {
			t.Fatalf("%s: no order.received event", src)
		}
		for _, ev := range evs {
			raw := string(ev.Payload)
			for _, esc := range []string{`\u001b`, `\u001d`, `\u0010`, `\u0000`, `\u007f`, `\n`, `\r`, `\t`} {
				if strings.Contains(raw, esc) {
					t.Errorf("%s: event %s carries %s: %s", src, ev.Type, esc, raw)
				}
			}
		}
	}
}

func TestNewOrderClipsText(t *testing.T) {
	long := func(n int) string { return strings.Repeat("é", n) }
	in := dirtyInput(SourceOnline)
	in.MoneyFlagged = true
	in.Customer.DisplayName = long(200)
	in.Notes = long(1000)
	in.Fulfillment.Address.Line1 = long(500)
	in.Fulfillment.Address.Notes = long(500)
	in.Items[0].Name = long(400)
	in.Items[0].Notes = long(400)
	in.Items[0].Children[0].Name = long(400)
	o, err := NewOrder(in)
	if err != nil {
		t.Fatal(err)
	}
	n := utf8.RuneCountInString
	it := o.Items()[0]
	got := map[string][2]int{
		"customer name": {n(o.Customer().DisplayName), MaxNameLen},
		"order notes":   {n(o.Notes()), MaxNoteLen},
		"address line":  {n(o.Fulfillment().Address.Line1), MaxAddressLineLen},
		"address notes": {n(o.Fulfillment().Address.Notes), MaxNoteLen},
		"item name":     {n(it.Name), MaxItemNameLen},
		"item notes":    {n(it.Notes), MaxNoteLen},
		"modifier name": {n(it.Children[0].Name), MaxItemNameLen},
	}
	for what, g := range got {
		if g[0] != g[1] {
			t.Errorf("%s: %d characters, want %d", what, g[0], g[1])
		}
	}
	if MaxNameLen != 80 || MaxNoteLen != 300 || MaxAddressLineLen != 120 {
		t.Errorf("limits = %d / %d / %d, want 80 / 300 / 120", MaxNameLen, MaxNoteLen, MaxAddressLineLen)
	}

	// A counter order is the Pi's own: stripped, but kept at the counter's limit.
	in = dirtyInput(SourceCounter)
	in.MoneyFlagged = true
	in.Notes = long(MaxCounterText)
	in.Items[0].Name = long(MaxCounterText)
	o, err = NewOrder(in)
	if err != nil {
		t.Fatal(err)
	}
	if n(o.Notes()) != MaxCounterText || n(o.Items()[0].Name) != MaxCounterText {
		t.Errorf("counter copy cut to %d / %d, want %d", n(o.Notes()), n(o.Items()[0].Name), MaxCounterText)
	}
}

// Clean text is stored exactly as it came (goldens, receipts, routing).
func TestNewOrderLeavesCleanTextAlone(t *testing.T) {
	in := NewOrderInput{
		TenantID: TenantID(uuid.New()), LocationID: LocationID(uuid.New()), Source: SourceOnline, SourceEventID: "evt-2",
		Customer:    Customer{DisplayName: "  José Núñez-O'Brien ", Phone: "(410) 555-1212", Email: "jose@example.com"},
		Fulfillment: Fulfillment{Type: FulfillmentDelivery, Address: &Address{Line1: "931 Taylor Ave", Line2: "Apt #4 ½", City: "Towson", State: "MD", PostalCode: "21286", Notes: "Gate code *1234# — ring twice 🍕"}},
		Items:       []LineItem{{ID: uuid.Must(uuid.NewV7()), Name: "(12 Medium) Jalapeño & Cheese", Size: "12 Medium", Quantity: 2, UnitPrice: 500, LineTotal: 1000, Notes: "half/half; extra crispy!"}},
		Money:       MoneyBreakdown{Subtotal: 1000, Total: 1000},
		Currency:    "USD", PlacedAt: time.Now(), Notes: "Leave at door. Thanks :)",
	}
	o, err := NewOrder(in)
	if err != nil {
		t.Fatal(err)
	}
	if o.Customer() != in.Customer || *o.Fulfillment().Address != *in.Fulfillment.Address || o.Notes() != in.Notes ||
		o.Items()[0].Name != in.Items[0].Name || o.Items()[0].Notes != in.Items[0].Notes || o.Items()[0].Size != in.Items[0].Size {
		t.Errorf("clean text changed:\n got %+v %+v %q %+v\nwant %+v %+v %q %+v", o.Customer(), *o.Fulfillment().Address, o.Notes(), o.Items()[0], in.Customer, *in.Fulfillment.Address, in.Notes, in.Items[0])
	}
}
