package staff

import (
	"encoding/json"
	"os"
	"testing"
)

// The effective limits main computes and sends the Pi (effective_limits),
// as the shared rule table says (quicc-ops contract/money-rules.json, byte
// copy in domain/order/testdata).
func TestMoneyRulesEffectiveLimits(t *testing.T) {
	b, err := os.ReadFile("../order/testdata/money-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	var tab struct {
		Settings json.RawMessage `json:"settings"`
		People   map[string]struct {
			Role      string     `json:"role"`
			Limits    Limits     `json:"limits"`
			MainMakes *bool      `json:"main_makes"`
			Absent    bool       `json:"absent"`
			Effective *Effective `json:"effective"`
		} `json:"people"`
		Effective []struct {
			ID       string          `json:"id"`
			Person   string          `json:"person"`
			Settings json.RawMessage `json:"settings"`
			Expect   *Effective      `json:"expect"`
		} `json:"effective"`
	}
	if err := json.Unmarshal(b, &tab); err != nil {
		t.Fatal(err)
	}
	settings := func(extra json.RawMessage) Settings {
		s := DefaultSettings()
		for _, raw := range []json.RawMessage{tab.Settings, extra} {
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &s); err != nil {
					t.Fatal(err)
				}
			}
		}
		return s
	}
	same := func(a, b *Effective) bool {
		if a == nil || b == nil {
			return a == nil && b == nil
		}
		eq := func(x, y *int64) bool { return (x == nil && y == nil) || (x != nil && y != nil && *x == *y) }
		eqi := func(x, y *int) bool { return (x == nil && y == nil) || (x != nil && y != nil && *x == *y) }
		return eq(a.RefundCents, b.RefundCents) && eqi(a.DiscountPct, b.DiscountPct)
	}
	// main's "unlimited" = both null (a manager); the table writes it null.
	norm := func(e Effective) *Effective {
		if e.RefundCents == nil && e.DiscountPct == nil {
			return nil
		}
		return &e
	}
	for _, c := range tab.Effective {
		p := tab.People[c.Person]
		got := norm(EffectiveLimits(Person{Role: p.Role, Limits: p.Limits}, settings(c.Settings)))
		if !same(got, c.Expect) {
			t.Errorf("%s %s: main's effective limits %+v, the table says %+v", c.ID, c.Person, got, c.Expect)
		}
	}
	// Each person's effective limits under the table's settings (what the Pi
	// and the terminal cases are written with).
	for name, p := range tab.People {
		if p.Absent || (p.MainMakes != nil && !*p.MainMakes) {
			continue
		}
		got := norm(EffectiveLimits(Person{Role: p.Role, Limits: p.Limits}, settings(nil)))
		if !same(got, p.Effective) {
			t.Errorf("person %s: main's effective limits %+v, the table says %+v", name, got, p.Effective)
		}
	}
}
