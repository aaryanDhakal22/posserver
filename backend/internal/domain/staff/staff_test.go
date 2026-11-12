package staff

import (
	"reflect"
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
	e = EffectiveLimits(Person{Role: RoleManager, Limits: Limits{RefundCents: &r}}, s)
	if *e.RefundCents != 500 || e.DiscountPct != nil {
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

func TestTerminalAppDefaults(t *testing.T) {
	d := DefaultSettings()
	if d.DefaultTab != "info" || d.ShowHistory || d.ButtonFont != "barlow" || d.CashierPriceDropPct != 20 || d.ClickSound ||
		d.PricesOnButtons || d.OutOfAreaMarginMi != 1 || d.NameOnPaidCounter || d.CardTips ||
		!reflect.DeepEqual(d.CardTipPercents, []int{15, 18, 20}) || d.EmployeeMealPct != 50 {
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
