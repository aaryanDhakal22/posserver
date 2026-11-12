package dayclose

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func i64(v int64) *int64 { return &v }

func opened() Drawer {
	b := int64(7)
	return Drawer{ID: uuid.New(), DeviceID: "dev-1", DeviceName: "Till 1", StaffID: uuid.NewString(), StaffName: "Cara S.",
		BatchID: &b, OpenedAt: time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC), StartCents: 15000}
}

func closed() Drawer {
	d := opened()
	at := d.OpenedAt.Add(6 * time.Hour)
	d.ClosedAt, d.CloseReason, d.CountedCents, d.ExpectedCents, d.OverShortCents = &at, CloseCounted, i64(23950), i64(24000), i64(-50)
	d.Denominations = []Denomination{{ValueCents: 2000, Count: 10}, {ValueCents: 100, Count: 39}, {ValueCents: 50, Count: 1}}
	d.CashSalesCents, d.PayInsCents, d.PayOutsCents, d.Payments = 9000, 1000, 1000, 4
	return d
}

func TestDrawerChecks(t *testing.T) {
	if d := opened(); d.CheckOpened() != nil {
		t.Fatal("a good opened drawer is refused")
	}
	if d := closed(); d.CheckClosed() != nil {
		t.Fatal("a good closed drawer is refused")
	}
	// Close batch: not counted, flagged.
	bc := closed()
	bc.CloseReason, bc.CountedCents, bc.OverShortCents, bc.Denominations, bc.Flagged = CloseBatchClose, nil, nil, nil, true
	if err := bc.CheckClosed(); err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]func(*Drawer){
		"no id":          func(d *Drawer) { d.ID = uuid.Nil },
		"no opened_at":   func(d *Drawer) { d.OpenedAt = time.Time{} },
		"negative start": func(d *Drawer) { d.StartCents = -1 },
		"huge start":     func(d *Drawer) { d.StartCents = MaxCents + 1 },
		"long name":      func(d *Drawer) { d.StaffName = string(make([]rune, 81)) },
		"no closed_at":   func(d *Drawer) { d.ClosedAt = nil },
		"bad reason":     func(d *Drawer) { d.CloseReason = "lost" },
		"neg counted":    func(d *Drawer) { d.CountedCents = i64(-1) },
		"bad denom":      func(d *Drawer) { d.Denominations = []Denomination{{ValueCents: 0, Count: 1}} },
		"neg sales":      func(d *Drawer) { d.CashSalesCents = -5 },
		"neg no sales":   func(d *Drawer) { d.NoSales = -1 },
	} {
		d := closed()
		f(&d)
		if d.CheckClosed() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestMovementCheck(t *testing.T) {
	good := Movement{ID: uuid.New(), DrawerID: uuid.New(), Kind: PayOut, AmountCents: 2500, Reason: " ice ", At: time.Now()}
	if err := good.Check(); err != nil || good.Reason != "ice" {
		t.Fatalf("%v %q", err, good.Reason)
	}
	for name, f := range map[string]func(*Movement){
		"no id":     func(m *Movement) { m.ID = uuid.Nil },
		"no drawer": func(m *Movement) { m.DrawerID = uuid.Nil },
		"kind":      func(m *Movement) { m.Kind = "refund" },
		"zero":      func(m *Movement) { m.AmountCents = 0 },
		"no at":     func(m *Movement) { m.At = time.Time{} },
	} {
		m := good
		f(&m)
		if m.Check() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseZAndSummary(t *testing.T) {
	id := uuid.New()
	raw, _ := json.Marshal(map[string]any{"id": id, "version": 1, "batch_id": 12, "business_date": "2026-11-11",
		"opened_at": "2026-11-11T15:00:00Z", "closed_at": "2026-11-12T09:00:00Z", "close_kind": "auto",
		"counts": map[string]any{"orders": 41}, "sales": map[string]any{"total_cents": 98765},
		"over_short_cents": -50, "reconcile": map[string]any{"difference_cents": 0}, "tenders": []any{}})
	z, err := ParseZ(raw)
	if err != nil {
		t.Fatal(err)
	}
	if z.ID != id || z.BatchID != 12 || z.BusinessDate.Format(time.DateOnly) != "2026-11-11" || z.ClosedAt == nil {
		t.Fatalf("%+v", z)
	}
	s := SummaryOf(z)
	if s.Orders != 41 || s.TotalCents != 98765 || s.OverShortCents != -50 || s.CloseKind != "auto" || s.BusinessDate != "2026-11-11" {
		t.Fatalf("%+v", s)
	}
	for _, bad := range []string{`[]`, `{}`, `{"id":"x","batch_id":1,"business_date":"2026-11-11"}`,
		`{"id":"` + id.String() + `","business_date":"2026-11-11"}`,
		`{"id":"` + id.String() + `","batch_id":0,"business_date":"2026-11-11"}`,
		`{"id":"` + id.String() + `","batch_id":3,"business_date":"11/11/2026"}`,
		`{"id":"` + id.String() + `","batch_id":3,"business_date":"2026-02-30"}`} {
		if _, err := ParseZ(json.RawMessage(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestCheckZLeavesDriverCashAndOnlineOut(t *testing.T) {
	// A day with a cash delivery (the driver collected $17.77 at the door: no payment row) and an online
	// order that arrived paid: main has only the counter payments; the check still matches.
	report := json.RawMessage(`{"tenders": [{"tender": "cash", "net_cents": 5317}, {"tender": "square_app", "net_cents": 3860},
		{"tender": "online", "net_cents": 1500}, {"tender": "driver_cash", "net_cents": 2177}], "reconcile": {"difference_cents": 0}}`)
	c := CheckZ(report, map[string]int64{"cash": 5317, "square_app": 3860})
	if !c.Checked || !c.Matches() || !c.Balanced() || c.MainDifference == nil || *c.MainDifference != 0 {
		t.Fatalf("check: %+v diff %v", c, c.MainDifference)
	}
	for _, tc := range c.Tenders {
		if NotCounterTenders[tc.Tender] {
			t.Fatalf("%s compared: %+v", tc.Tender, c.Tenders)
		}
	}
	// A real mismatch on a counter tender is still caught.
	c = CheckZ(report, map[string]int64{"cash": 5000, "square_app": 3860})
	if c.Matches() || *c.MainDifference != -317 {
		t.Fatalf("mismatch: %+v %v", c, *c.MainDifference)
	}
}
