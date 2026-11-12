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
