package order

import (
	"encoding/json"
	"os"
	"slices"
	"sort"
	"testing"

	"github.com/google/uuid"
)

// The shared rule table (quicc-ops contract/money-rules.json; this is its
// byte copy, scripts/sync-money-rules.sh): every case main checks, main's
// flags as the table says. The Pi and the terminal run the same file.

type rulePerson struct {
	Role      string `json:"role"`
	Absent    bool   `json:"absent"`
	Effective *struct {
		RefundCents int64 `json:"refund_cents"`
	} `json:"effective"`
}

type rulePay struct {
	Tender   string `json:"tender"`
	Cents    int64  `json:"cents"`
	Tip      int64  `json:"tip"`
	Refund   bool   `json:"refund"`
	By       string `json:"by"`
	Approved bool   `json:"approved"`
}

type ruleCase struct {
	ID    string `json:"id"`
	Note  string `json:"note"`
	By    string `json:"by"`
	Cents int64  `json:"cents"`
	Order struct {
		Batch    string    `json:"batch"`
		Payments []rulePay `json:"payments"`
	} `json:"order"`
	Refund         rulePay   `json:"refund"`
	Payment        rulePay   `json:"payment"`
	TipAfter       bool      `json:"tip_after"`
	Main           *[]string `json:"main"`
	MainIfApproved []string  `json:"main_if_approved"`
}

type ruleTable struct {
	People         map[string]rulePerson `json:"people"`
	Refunds        []ruleCase            `json:"refunds"`
	GeneralRefunds []ruleCase            `json:"general_refunds"`
	Comps          []ruleCase            `json:"comps"`
	Tips           []ruleCase            `json:"tips"`
}

func loadRules(t *testing.T) ruleTable {
	t.Helper()
	b, err := os.ReadFile("testdata/money-rules.json")
	if err != nil {
		t.Fatal(err)
	}
	var tab ruleTable
	if err := json.Unmarshal(b, &tab); err != nil {
		t.Fatal(err)
	}
	return tab
}

func codesOf(flags []MoneyFlag) []string {
	out := []string{}
	for _, f := range flags {
		out = append(out, f.Code)
	}
	sort.Strings(out)
	return out
}

func sorted(s []string) []string {
	out := append([]string{}, s...)
	sort.Strings(out)
	return out
}

func TestMoneyRules(t *testing.T) {
	tab := loadRules(t)
	ids := map[string]uuid.UUID{}
	people := map[uuid.UUID]Refunder{}
	for name, p := range tab.People {
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(name))
		ids[name] = id
		if p.Absent {
			continue // not at the store (main's staff table has no row)
		}
		var limit *int64
		if p.Effective != nil {
			limit = &p.Effective.RefundCents
		}
		people[id] = RefunderOf(p.Role, limit)
	}
	mgr := ids["mia"].String()
	staffOf := func(name string) *uuid.UUID { id := ids[name]; return &id }
	// The Pi's wire (main §2.5zb): a refund negative with its tip folded in.
	wire := func(p rulePay) (int64, int64) {
		if p.Refund {
			return -(p.Cents + p.Tip), 0
		}
		return p.Cents, p.Tip
	}
	check := func(t *testing.T, c ruleCase, got func(approvedBy string) []MoneyFlag) {
		t.Helper()
		if c.Main == nil {
			return // main never sees it (the Pi refuses it before any event)
		}
		if g, w := codesOf(got("")), sorted(*c.Main); !slices.Equal(g, w) {
			t.Errorf("%s (%s): main flags %v, the table says %v", c.ID, c.Note, g, w)
		}
		if g, w := codesOf(got(mgr)), sorted(c.MainIfApproved); !slices.Equal(g, w) {
			t.Errorf("%s (%s): approved, main flags %v, the table says %v", c.ID, c.Note, g, w)
		}
	}
	t.Run("refunds", func(t *testing.T) {
		for _, c := range tab.Refunds {
			o := RuleOrder{Earlier: c.Order.Batch == "older"}
			for _, p := range c.Order.Payments {
				a, tip := wire(p)
				x := PriorPayment{Method: p.Tender, Amount: a, Tip: tip, By: Who(people, staffOf(p.By))}
				if p.Approved {
					x.ApprovedBy = mgr
				}
				if !p.Refund {
					o.Total += p.Cents
				}
				o.Prior = append(o.Prior, x)
			}
			r := c.Refund
			r.Refund = true
			check(t, c, func(approvedBy string) []MoneyFlag {
				a, tip := wire(r)
				p := CheckedPayment{Method: r.Tender, Amount: Money(a), Tip: Money(tip), StaffID: staffOf(c.By), ApprovedBy: approvedBy}
				return PaymentFlags(o, p, Who(people, p.StaffID))
			})
		}
	})
	t.Run("general refunds", func(t *testing.T) {
		for _, c := range tab.GeneralRefunds {
			check(t, c, func(approvedBy string) []MoneyFlag {
				p := CheckedPayment{Method: "cash", Amount: Money(-c.Cents), StaffID: staffOf(c.By), ApprovedBy: approvedBy}
				return GeneralRefundFlags(p, Who(people, p.StaffID))
			})
		}
	})
	t.Run("tips", func(t *testing.T) {
		if len(tab.Tips) == 0 {
			t.Fatal("the table has no tips section")
		}
		for _, c := range tab.Tips {
			check(t, c, func(approvedBy string) []MoneyFlag {
				p := CheckedPayment{Method: c.Payment.Tender, Amount: Money(c.Payment.Cents), Tip: Money(c.Payment.Tip),
					StaffID: staffOf(c.By), ApprovedBy: approvedBy}
				o := RuleOrder{Total: c.Payment.Cents}
				if c.TipAfter {
					// The Pi's tip afterwards: an amount-0 payment with the tip, on the tipped payment's tender.
					o.Prior = []PriorPayment{{Method: c.Payment.Tender, Amount: c.Payment.Cents, By: Who(people, staffOf(c.By))}}
					p.Amount = 0
				}
				return PaymentFlags(o, p, Who(people, p.StaffID))
			})
		}
	})
	t.Run("comps", func(t *testing.T) {
		for _, c := range tab.Comps {
			check(t, c, func(approvedBy string) []MoneyFlag {
				p := CheckedPayment{Method: "comp", Amount: Money(c.Cents), StaffID: staffOf(c.By), ApprovedBy: approvedBy}
				return PaymentFlags(RuleOrder{Total: c.Cents}, p, Who(people, p.StaffID))
			})
		}
	})
}

// A snapshot's comp is "other" (the Pi's PaymentSummary method): the same
// rule as "comp".
func TestMoneyRulesCompAsOther(t *testing.T) {
	cashier := RefunderOf("cashier", new(int64(2000)))
	o := RuleOrder{Total: 1377, Prior: []PriorPayment{{Method: "other", Amount: 1377, By: cashier}}}
	if f := PaymentFlags(o, CheckedPayment{Method: "comp", Amount: -1377}, cashier); len(f) != 0 {
		t.Fatalf("a comp reversed: %v", f)
	}
	driver := RefunderOf("driver", new(int64(0)))
	if f := codesOf(PaymentFlags(RuleOrder{Total: 500}, CheckedPayment{Method: "other", Amount: 500}, driver)); !slices.Equal(f, []string{FlagCompNeedsManager}) {
		t.Fatalf("a driver's comp: %v", f)
	}
	// A driver's door tip (main's own row) is not the order's money.
	o = RuleOrder{Total: 1000, Prior: []PriorPayment{{Method: "cash", Amount: 1000, By: cashier}, {Method: "door_tip", Tip: 500}}}
	if f := codesOf(PaymentFlags(o, CheckedPayment{Method: "cash", Amount: -1200}, RefunderOf("manager", nil))); !slices.Equal(f, []string{FlagRefundExceedsPaid}) {
		t.Fatalf("door tip counted: %v", f)
	}
}
