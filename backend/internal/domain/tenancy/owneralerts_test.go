package tenancy

import (
	"testing"
	"time"
)

func TestOwnerAlertSettingsDefaultsAndClamps(t *testing.T) {
	d := OwnerAlertSettingsFrom(nil)
	if !d.Enabled || d.Offline != 5*time.Minute || d.Cooldown != 30*time.Minute || d.MaxPerHour != 6 || d.MaxPerDay != 20 {
		t.Fatalf("defaults = %+v", d)
	}
	s := OwnerAlertSettingsFrom(Config{
		OwnerAlertsKey:               false,
		OwnerAlertOfflineMinutesKey:  float64(9),
		OwnerAlertCooldownMinutesKey: float64(5), // never under 30
		OwnerAlertMaxPerHourKey:      float64(500),
		OwnerAlertMaxPerDayKey:       "lots", // not a number: the default
	})
	if s.Enabled || s.Offline != 9*time.Minute || s.Cooldown != 30*time.Minute || s.MaxPerHour != 60 || s.MaxPerDay != 20 {
		t.Fatalf("read = %+v", s)
	}
	if got := OwnerAlertSettingsFrom(Config{OwnerAlertOfflineMinutesKey: float64(0)}).Offline; got != time.Minute {
		t.Fatalf("offline clamps to 1 min, got %v", got)
	}
	if got := OwnerAlertSettingsFrom(Config{OwnerAlertOfflineMinutesKey: 500}).Offline; got != 120*time.Minute {
		t.Fatalf("offline clamps to 120 min, got %v", got)
	}
}

func TestOwnerAlertsOpen(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	at := func(h, m int) time.Time { return time.Date(2026, 11, 12, h, m, 0, 0, ny) } // a Thursday
	// No hours: 09:00-02:00.
	for _, c := range []struct {
		h, m int
		open bool
	}{{8, 59, false}, {9, 0, true}, {20, 42, true}, {1, 59, true}, {2, 0, false}, {5, 0, false}} {
		if got := OwnerAlertsOpen(nil, at(c.h, c.m), ny); got != c.open {
			t.Fatalf("fallback %02d:%02d open = %v", c.h, c.m, got)
		}
	}
	// The store's own hours, no grace.
	cfg := Config{OpeningHoursKey: map[string]any{"thu": []any{map[string]any{"open": "11:00", "close": "22:00"}}}}
	if OwnerAlertsOpen(cfg, at(10, 59), ny) || !OwnerAlertsOpen(cfg, at(11, 0), ny) || OwnerAlertsOpen(cfg, at(22, 0), ny) {
		t.Fatal("the store's hours, no grace")
	}
	// Invalid hours: the fallback.
	bad := Config{OpeningHoursKey: map[string]any{"thu": []any{map[string]any{"open": "nope", "close": "22:00"}}}}
	if !OwnerAlertsOpen(bad, at(23, 0), ny) {
		t.Fatal("invalid hours = 09:00-02:00")
	}
}
