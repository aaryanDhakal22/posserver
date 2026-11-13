package staff

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestValidPin(t *testing.T) {
	for pin, ok := range map[string]bool{"12": true, "0000": true, "123": true, "1": false, "12345": false, "12a": false, "": false, "١٢": false} {
		if ValidPin(pin) != ok {
			t.Errorf("ValidPin(%q) = %v", pin, !ok)
		}
	}
}

func TestEffectiveLimits(t *testing.T) {
	s := DefaultSettings()
	s.CashierRefundCents, s.CashierDiscountPct = 2000, 15
	e := EffectiveLimits(Person{Role: RoleCashier}, s)
	if *e.RefundCents != 2000 || *e.DiscountPct != 15 {
		t.Errorf("cashier: %+v", e)
	}
	if e := EffectiveLimits(Person{Role: RoleManager}, s); e.RefundCents != nil || e.DiscountPct != nil {
		t.Errorf("manager is unlimited: %+v", e)
	}
	if e := EffectiveLimits(Person{Role: RoleDriver}, s); *e.RefundCents != 0 || *e.DiscountPct != 0 {
		t.Errorf("driver: %+v", e)
	}
	r, d := int64(500), 30
	// A manager's own limits are ignored (the Pi never applied them).
	e = EffectiveLimits(Person{Role: RoleManager, Limits: Limits{RefundCents: &r}}, s)
	if e.RefundCents != nil || e.DiscountPct != nil {
		t.Errorf("a manager stays unlimited: %+v", e)
	}
	e = EffectiveLimits(Person{Role: RoleDriver, Limits: Limits{RefundCents: &r}}, s)
	if *e.RefundCents != 500 || *e.DiscountPct != 0 {
		t.Errorf("own limit over the role's: %+v", e)
	}
	e = EffectiveLimits(Person{Role: RoleCashier, Limits: Limits{DiscountPct: &d}}, s)
	if *e.RefundCents != 2000 || *e.DiscountPct != 30 {
		t.Errorf("cashier override: %+v", e)
	}
}

func TestSettingsValidate(t *testing.T) {
	if err := DefaultSettings().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []func(*Settings){
		func(s *Settings) { s.AutoLockMinutes = 0 },
		func(s *Settings) { s.CounterStart = 0 },
		func(s *Settings) { s.CashierDiscountPct = 101 },
		func(s *Settings) { s.CashierRefundCents = -1 },
		func(s *Settings) { s.ReceiptAutoDoneSeconds = 601 },
		func(s *Settings) { s.AutoCloseBatchAt = "4:00" },
		func(s *Settings) { s.AutoCloseBatchAt = "24:00" },
		func(s *Settings) { s.AutoCloseBatchAt = "04:60" },
		func(s *Settings) { s.AutoCloseBatchAt = "4am" },
		func(s *Settings) { s.DefaultTab = "drawer" },
		func(s *Settings) { s.DefaultTab = "" },
		func(s *Settings) { s.ButtonFont = "comic_sans" },
		func(s *Settings) { s.CashierPriceDropPct = 101 },
		func(s *Settings) { s.OutOfAreaMarginMi = -0.1 },
		func(s *Settings) { s.OutOfAreaMarginMi = 10.5 },
		func(s *Settings) { s.CardTipPercents = []int{} },
		func(s *Settings) { s.CardTipPercents = []int{1, 2, 3, 4, 5, 6, 7} },
		func(s *Settings) { s.CardTipPercents = []int{15, 101} },
		func(s *Settings) { s.EmployeeMealPct = -1 },
		func(s *Settings) { s.CashierPayoutCents = -1 },
		func(s *Settings) { s.CashierPayoutCents = 100_001 },
		func(s *Settings) { s.DefaultPickupMinutes = -1 },
		func(s *Settings) { s.DefaultPickupMinutes = 241 },
		func(s *Settings) { s.AreaCodes = []string{} },
		func(s *Settings) { s.AreaCodes = []string{"41"} },
		func(s *Settings) { s.AreaCodes = []string{"110"} },
		func(s *Settings) { s.AreaCodes = []string{"41a"} },
		func(s *Settings) { s.AreaCodes = []string{"4100"} },
	} {
		s := DefaultSettings()
		f(&s)
		if s.Validate() == nil {
			t.Errorf("accepted %+v", s)
		}
	}
}

func TestAutoCloseBatchAt(t *testing.T) {
	if DefaultSettings().AutoCloseBatchAt != "04:00" {
		t.Fatal("on by default at 4:00 am")
	}
	for _, v := range []string{"", "00:00", "04:00", "23:59"} {
		s := DefaultSettings()
		s.AutoCloseBatchAt = v
		if err := s.Validate(); err != nil {
			t.Errorf("%q: %v", v, err)
		}
	}
}

// The owner's defaults for every store (2026-11-12): auto-lock 5 min, a
// cashier discounts up to 20 %, lowers a price up to 20 %, the employee
// meal is 20 % off.
func TestOwnerDefaults(t *testing.T) {
	d := DefaultSettings()
	got := [4]int{d.AutoLockMinutes, d.CashierDiscountPct, d.CashierPriceDropPct, d.EmployeeMealPct}
	if got != [4]int{5, 20, 20, 20} {
		t.Fatalf("lock, discount, price drop, meal = %v", got)
	}
	if e := RoleDefaults(RoleCashier, d); *e.DiscountPct != 20 || *e.RefundCents != 2000 {
		t.Fatalf("cashier role defaults: %+v", e)
	}
}

func TestTerminalAppDefaults(t *testing.T) {
	d := DefaultSettings()
	if d.DefaultTab != "info" || d.ShowHistory || d.ButtonFont != "barlow" || d.CashierPriceDropPct != 20 || d.ClickSound ||
		d.PricesOnButtons || d.OutOfAreaMarginMi != 1 || d.NameOnPaidCounter || d.CardTips ||
		!reflect.DeepEqual(d.CardTipPercents, []int{15, 18, 20}) || d.EmployeeMealPct != 20 {
		t.Fatalf("defaults: %+v", d)
	}
	for _, tab := range DefaultTabs {
		s := DefaultSettings()
		s.DefaultTab = tab
		if err := s.Validate(); err != nil {
			t.Errorf("%s: %v", tab, err)
		}
	}
	s := DefaultSettings()
	s.CardTipPercents, s.OutOfAreaMarginMi = nil, 1.26
	s.Normalize()
	if !reflect.DeepEqual(s.CardTipPercents, []int{15, 18, 20}) || s.OutOfAreaMarginMi != 1.3 {
		t.Fatalf("normalize: %+v", s)
	}
}

func TestDrawerAndPhoneDefaults(t *testing.T) {
	d := DefaultSettings()
	if d.CashierPayoutCents != 2000 || d.DefaultPickupMinutes != 15 || !reflect.DeepEqual(d.AreaCodes, []string{"410", "443", "667"}) {
		t.Fatalf("defaults: %+v", d)
	}
	s := DefaultSettings()
	s.AreaCodes = nil
	s.Normalize()
	if !reflect.DeepEqual(s.AreaCodes, []string{"410", "443", "667"}) {
		t.Fatalf("null area codes = the defaults: %v", s.AreaCodes)
	}
	s.AreaCodes = []string{" 202", "410", "202", "301"}
	s.Normalize()
	if !reflect.DeepEqual(s.AreaCodes, []string{"202", "410", "301"}) || s.Validate() != nil {
		t.Fatalf("trimmed + deduped: %v", s.AreaCodes)
	}
	many := make([]string, 21)
	for i := range many {
		many[i] = fmt.Sprintf("%d", 200+i)
	}
	s.AreaCodes = many
	if s.Validate() == nil {
		t.Fatal("21 area codes accepted")
	}
	s.AreaCodes, s.CashierPayoutCents, s.DefaultPickupMinutes = []string{"212"}, 0, 0
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

// The owner's defaults of 2026-11-12: a cashier refunds cash up to $20 per
// order; drivers' claim follows the Pi's promised time (no setting).
func TestCashierRefundDefault(t *testing.T) {
	s := DefaultSettings()
	if s.CashierRefundCents != 2000 {
		t.Fatalf("default: %d", s.CashierRefundCents)
	}
	if e := EffectiveLimits(Person{Role: RoleCashier}, s); *e.RefundCents != 2000 {
		t.Fatalf("cashier: %+v", e)
	}
}

// The cash left in each drawer at opening (the owner, 2026-11-12): $100.
func TestDrawerStartDefault(t *testing.T) {
	s := DefaultSettings()
	if s.DrawerStartCents != 10000 {
		t.Fatalf("default %d", s.DrawerStartCents)
	}
	s.DrawerStartCents = -1
	if s.Validate() == nil {
		t.Fatal("negative accepted")
	}
	s.DrawerStartCents = MaxDrawerStartCents + 1
	if s.Validate() == nil {
		t.Fatal("too much accepted")
	}
}

// The owner's waste reasons (2026-11-12): defaults, normalised, bounded.
func TestWasteReasons(t *testing.T) {
	s := DefaultSettings()
	if strings.Join(s.WasteReasons, "|") != "Never picked up|Made wrong / remade|Customer complaint|Burnt / dropped" {
		t.Fatalf("defaults: %q", s.WasteReasons)
	}
	s.WasteReasons = []string{"  Dropped  on   floor ", "", "dropped on floor", "Cold"}
	s.Normalize()
	if strings.Join(s.WasteReasons, "|") != "Dropped on floor|Cold" {
		t.Fatalf("normalised: %q", s.WasteReasons)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.WasteReasons = []string{}
	if s.Validate() == nil {
		t.Fatal("no reasons accepted")
	}
	s.WasteReasons = []string{strings.Repeat("x", MaxWasteReasonLen+1)}
	if s.Validate() == nil {
		t.Fatal("too long accepted")
	}
	var nilList Settings = DefaultSettings()
	nilList.WasteReasons = nil
	nilList.Normalize()
	if len(nilList.WasteReasons) != 4 {
		t.Fatalf("nil = the defaults: %q", nilList.WasteReasons)
	}
}

// The owner's void reasons (2026-11-12, round 2): picked, never typed;
// defaults with "Other", normalised and bounded like the waste reasons.
func TestVoidReasons(t *testing.T) {
	s := DefaultSettings()
	if strings.Join(s.VoidReasons, "|") != "Customer cancelled|Wrong order|Duplicate|Took too long|Other" {
		t.Fatalf("defaults: %q", s.VoidReasons)
	}
	s.VoidReasons = []string{"  Changed   mind ", "", "changed mind", "Other"}
	s.Normalize()
	if strings.Join(s.VoidReasons, "|") != "Changed mind|Other" {
		t.Fatalf("normalised: %q", s.VoidReasons)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("valid: %v", err)
	}
	s.VoidReasons = []string{}
	if err := s.Validate(); err == nil {
		t.Fatal("an empty list is refused")
	}
	s.VoidReasons = []string{strings.Repeat("x", MaxWasteReasonLen+1)}
	if err := s.Validate(); err == nil {
		t.Fatal("a reason too long is refused")
	}
	var old Settings
	old.Normalize() // a row saved before the field existed
	if len(old.VoidReasons) != 5 {
		t.Fatalf("nil = the defaults: %q", old.VoidReasons)
	}
}

// The owner's X of 2026-11-13: a forced close writes unpaid counter checks
// off as waste (default) or carries them.
func TestCloseUnpaidCounterAction(t *testing.T) {
	d := DefaultSettings()
	if d.CloseUnpaidCounterAction != CloseUnpaidWaste {
		t.Fatalf("default %q", d.CloseUnpaidCounterAction)
	}
	for _, v := range []string{CloseUnpaidWaste, CloseUnpaidCarry} {
		s := DefaultSettings()
		s.CloseUnpaidCounterAction = v
		if err := s.Validate(); err != nil {
			t.Fatalf("%s: %v", v, err)
		}
	}
	s := DefaultSettings()
	s.CloseUnpaidCounterAction = "keep"
	if err := s.Validate(); err == nil {
		t.Fatal("keep accepted")
	}
	s.CloseUnpaidCounterAction = ""
	s.Normalize()
	if s.CloseUnpaidCounterAction != CloseUnpaidWaste || s.Validate() != nil {
		t.Fatalf("a row saved before the field: %q", s.CloseUnpaidCounterAction)
	}
}
