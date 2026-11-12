package order

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func counterSnap() CounterSnapshot {
	k := 7
	return CounterSnapshot{Snapshot: Snapshot{
		ID: uuid.Must(uuid.NewV7()).String(), Source: "counter", SourceEventID: "key-1", Status: "accepted",
		Money:       MoneyDTO{Subtotal: 1000, Tax: 60, Total: 1060},
		Fulfillment: FulfillmentDTO{Type: "counter"},
		Items: []LineItemDTO{{Name: "Cheese Pizza", Quantity: 1, UnitPrice: 1000, LineTotal: 1000,
			Children: []LineItemDTO{{Name: "Pepperoni (1st half)", Quantity: 1, Portion: "first", Mode: "add"}}}},
		PlacedAt: time.Now(), KitchenNumber: &k,
	}, StaffID: uuid.NewString(), DeviceID: "term-1",
		CounterPayments: []CounterPayment{{ID: uuid.NewString(), Method: "cash", AmountCents: 1060}}}
}

func TestNewCounterOrder(t *testing.T) {
	sc := Scope{TenantID: TenantID(uuid.New()), LocationID: LocationID(uuid.New())}
	s := counterSnap()
	co, err := NewCounterOrder(sc, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	o := co.Order
	if uuid.UUID(o.ID()).String() != s.ID || o.Source() != SourceCounter || o.SourceEventID() != "key-1" || o.TenantID() != sc.TenantID {
		t.Errorf("order: %+v", o.Snapshot())
	}
	if co.Floor.Status != StatusAccepted || *co.Floor.KitchenNumber != 7 || len(co.Payments) != 1 || co.Payments[0].Status != PaymentCaptured {
		t.Errorf("floor / payments: %+v %+v", co.Floor, co.Payments)
	}
	if o.ApplyFloorUpdate(co.Floor) != FloorApplied || o.Status() != StatusAccepted || *o.KitchenNumber() != 7 {
		t.Errorf("floor not applied: %s", o.Status())
	}
	if o.Items()[0].Children[0].Portion != "first" || o.SourceMetadata()["device_id"] != "term-1" {
		t.Errorf("lines / meta: %+v", o.Items())
	}

	bad := []func(*CounterSnapshot){
		func(s *CounterSnapshot) { s.ID = "nope" },
		func(s *CounterSnapshot) { s.Source = "online" },
		func(s *CounterSnapshot) { s.Status = "scheduled" },
		func(s *CounterSnapshot) { s.Fulfillment.Type = "dine_in" },
		func(s *CounterSnapshot) { s.Items = nil },
		func(s *CounterSnapshot) { s.Money.Total = -1 },
		func(s *CounterSnapshot) { s.Items[0].Children[0].Mode = "double" },
		func(s *CounterSnapshot) { s.CounterPayments[0].Method = "bitcoin" },
		func(s *CounterSnapshot) { s.CounterPayments[0].TipCents = -1 },
		func(s *CounterSnapshot) { s.StaffID = "x" },
	}
	for i, f := range bad {
		s := counterSnap()
		f(&s)
		if _, err := NewCounterOrder(sc, s, nil); !errors.Is(err, ErrBadCounterOrder) {
			t.Errorf("case %d: err = %v", i, err)
		}
	}
}

// The snapshot's payments are the counter's full ones (not the summary shape).
func TestCounterSnapshotPaymentsJSON(t *testing.T) {
	var s CounterSnapshot
	raw := `{"id":"x","payments":[{"id":"p","method":"card","amount_cents":-500,"tip_cents":100,"ref":"sq_1"}]}`
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.CounterPayments) != 1 || s.CounterPayments[0].Ref != "sq_1" || s.CounterPayments[0].TipCents != 100 {
		t.Errorf("payments: %+v", s.CounterPayments)
	}
	p := s.CounterPayments[0]
	p.ID = uuid.NewString()
	cp, err := p.Check()
	if err != nil || cp.Status != PaymentRefunded || cp.Amount != -500 {
		t.Errorf("refund: %+v %v", cp, err)
	}
}

func TestApplyVoid(t *testing.T) {
	co, _ := NewCounterOrder(Scope{TenantID: TenantID(uuid.New()), LocationID: LocationID(uuid.New())}, counterSnap(), nil)
	o := co.Order
	o.ApplyFloorUpdate(FloorUpdate{Status: StatusCompleted, At: time.Now()})
	staff := uuid.New()
	if o.ApplyVoid("customer left", &staff, time.Now()) != FloorApplied || o.Status() != StatusCancelled {
		t.Fatalf("void a completed sale: %s", o.Status())
	}
	h := o.StatusHistory()
	if last := h[len(h)-1]; last.Reason != "void: customer left" || last.ActorID != staff {
		t.Errorf("history: %+v", last)
	}
	if o.ApplyVoid("again", nil, time.Now()) != FloorStale {
		t.Error("a second void is stale")
	}
}

// A delivery fee keeps the driver's share (the split), on the order and in
// the snapshot the Pi gets; a share above the fee is dropped.
func TestCounterDeliveryDriverShare(t *testing.T) {
	sc := Scope{TenantID: TenantID(uuid.New()), LocationID: LocationID(uuid.New())}
	s := counterSnap()
	s.Fulfillment = FulfillmentDTO{Type: "delivery", Address: &AddressDTO{Line1: "1 Main St", City: "Towson"}}
	s.Money = MoneyDTO{Subtotal: 1000, Tax: 60, Fees: []FeeDTO{{Type: "delivery", Label: "Delivery fee", Amount: 400, DriverCents: 300}}, Total: 1460}
	s.CounterPayments[0].AmountCents = 1460
	co, err := NewCounterOrder(sc, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f := co.Order.Money().Fees[0]; f.DriverCents != 300 || f.Amount != 400 {
		t.Fatalf("fee: %+v", f)
	}
	raw, _ := json.Marshal(co.Order.Snapshot().Money.Fees)
	if string(raw) != `[{"type":"delivery","label":"Delivery fee","amount_cents":400,"driver_cents":300}]` {
		t.Fatalf("snapshot fees: %s", raw)
	}
	s.Money.Fees[0].DriverCents = 401
	co, _ = NewCounterOrder(sc, s, nil)
	if f := co.Order.Money().Fees[0]; f.DriverCents != 0 {
		t.Fatalf("share over the fee kept: %+v", f)
	}
	if len(co.Flags) != 1 || co.Flags[0].Code != FlagDriverShareOverFee {
		t.Fatalf("the dropped share is flagged (M-13): %+v", co.Flags)
	}
}

// M-2 / M-13: money that doesn't add up is stored and flagged, never refused
// (the Pi's drawer has moved); exact, no cent of slack.
func TestCounterMoneyFlags(t *testing.T) {
	sc := Scope{TenantID: TenantID(uuid.New()), LocationID: LocationID(uuid.New())}
	s := counterSnap()
	co, err := NewCounterOrder(sc, s, nil)
	if err != nil || len(co.Flags) != 0 {
		t.Fatalf("clean order: %v %+v", err, co.Flags)
	}
	s.Money.Total = 1061 // one cent off
	co, err = NewCounterOrder(sc, s, nil)
	if err != nil || len(co.Flags) != 1 || co.Flags[0].Code != FlagMoneyMismatch || co.Flags[0].Detail["difference_cents"] != int64(1) {
		t.Fatalf("1 cent off: %v %+v", err, co.Flags)
	}
	if co.Order.Money().Total != 1061 {
		t.Fatalf("stored as sent: %d", co.Order.Money().Total)
	}
	s = counterSnap()
	s.Items[0].LineTotal = 900 // lines 900, subtotal 1000
	co, err = NewCounterOrder(sc, s, nil)
	if err != nil || len(co.Flags) != 1 || co.Flags[0].Code != FlagLinesSubtotal {
		t.Fatalf("lines vs subtotal: %v %+v", err, co.Flags)
	}
}

// The vendor keeps a cent of slack; every other source must add up exactly.
func TestMoneyReconcilesFor(t *testing.T) {
	m := MoneyBreakdown{Subtotal: 1000, Tax: 60, Total: 1061}
	if !moneyReconcilesFor(SourceOnline, m) {
		t.Error("vendor: a cent of slack")
	}
	for _, src := range []Source{SourceCounter, "web", "operator"} {
		if moneyReconcilesFor(src, m) {
			t.Errorf("%s: must be exact", src)
		}
	}
}

// agent 2.10aa: an online / website order the store edited comes back as
// order.changed with its own source; main checks its lines and money like a
// counter order's and leaves its channel fields (status, payments) alone.
func TestNewChangedOrderOnline(t *testing.T) {
	sc := Scope{TenantID: TenantID(uuid.New()), LocationID: LocationID(uuid.New())}
	for _, src := range []string{"online", "web"} {
		s := counterSnap()
		s.Source, s.Status, s.SourceEventID = src, "received", "vendor-123"
		s.Fulfillment = FulfillmentDTO{Type: "delivery", Address: &AddressDTO{Line1: "1 Test St", City: "Towson"}}
		s.CounterPayments = []CounterPayment{{ID: "not-even-a-uuid", Method: "online"}} // ignored for a channel order
		s.PlacedAt = time.Time{}
		s.ReceivedAt = time.Now()
		s.Notes = string(make([]byte, MaxCounterText+50))
		co, got, err := NewChangedOrder(sc, s, nil)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if string(got) != src || len(co.Payments) != 0 || co.Order.Money().Total != 1060 || len(co.Order.Items()) != 1 {
			t.Errorf("%s: source %q payments %d money %+v", src, got, len(co.Payments), co.Order.Money())
		}
		if a := co.Order.Fulfillment().Address; a == nil || a.Line1 != "1 Test St" {
			t.Errorf("%s: address %+v", src, a)
		}
	}
	// A counter snapshot is checked exactly as order.placed's.
	s := counterSnap()
	s.Status = "scheduled"
	if _, _, err := NewChangedOrder(sc, s, nil); !errors.Is(err, ErrBadCounterOrder) {
		t.Errorf("counter checks skipped: %v", err)
	}
	co, got, err := NewChangedOrder(sc, counterSnap(), nil)
	if err != nil || got != SourceCounter || len(co.Payments) != 1 {
		t.Errorf("counter: %v %q", err, got)
	}
	// The channel order's lines are still checked.
	s = counterSnap()
	s.Source = "online"
	s.Items[0].Quantity = 0
	if _, _, err := NewChangedOrder(sc, s, nil); !errors.Is(err, ErrBadCounterOrder) {
		t.Errorf("bad line passed: %v", err)
	}
}

// Tips are card only (money-rules tips): cash_tip on a cash / comp tip,
// also a tip added afterwards (an amount-0 payment); never on a refund.
func TestPaymentFlagsCashTip(t *testing.T) {
	cashier := RefunderOf("cashier", new(int64(2000)))
	codes := func(o RuleOrder, p CheckedPayment) []string { return codesOf(PaymentFlags(o, p, cashier)) }
	if f := codes(RuleOrder{Total: 1500}, CheckedPayment{Method: "cash", Amount: 1500, Tip: 300}); len(f) != 1 || f[0] != FlagCashTip {
		t.Errorf("cash tip: %v", f)
	}
	if f := codes(RuleOrder{Total: 1500}, CheckedPayment{Method: "card", Amount: 1500, Tip: 300}); len(f) != 0 {
		t.Errorf("card tip: %v", f)
	}
	o := RuleOrder{Total: 1500, Prior: []PriorPayment{{Method: "cash", Amount: 1500, Tip: 300, By: cashier}}}
	if f := codes(o, CheckedPayment{Method: "cash", Amount: -1800}); len(f) != 0 {
		t.Errorf("an old cash tip refunded: %v", f)
	}
}
