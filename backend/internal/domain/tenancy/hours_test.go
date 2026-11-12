package tenancy

import (
	"testing"
	"time"
)

func TestOpeningHours(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	h := OpeningHours{
		"mon": {{Open: "11:00", Close: "23:00"}},
		"fri": {{Open: "11:00", Close: "01:00"}}, // past midnight
	}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, ny)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	// 2026-10-19 is a Monday, 2026-10-23 a Friday.
	for _, c := range []struct {
		when string
		open bool
	}{
		{"2026-10-19 10:00", false},
		{"2026-10-19 10:50", true}, // grace 15 min
		{"2026-10-19 12:00", true},
		{"2026-10-19 23:10", true},
		{"2026-10-19 23:20", false},
		{"2026-10-20 12:00", false}, // tuesday closed
		{"2026-10-24 00:30", true},  // friday's overnight period on saturday
		{"2026-10-24 01:20", false},
	} {
		if got := h.IsOpen(at(c.when), ny, 15*time.Minute); got != c.open {
			t.Errorf("%s: open=%v, want %v", c.when, got, c.open)
		}
	}
	bad := []OpeningHours{
		{"funday": {{Open: "11:00", Close: "12:00"}}},
		{"mon": {{Open: "25:00", Close: "12:00"}}},
		{"mon": {{Open: "11:00", Close: "11:00"}}},
	}
	for _, b := range bad {
		if b.Validate() == nil {
			t.Errorf("%v should not validate", b)
		}
	}
	if _, ok, err := HoursFromConfig(Config{}); ok || err != nil {
		t.Errorf("empty config: ok=%v err=%v", ok, err)
	}
	cfg := Config{OpeningHoursKey: map[string]any{"mon": []any{map[string]any{"open": "11:00", "close": "22:00"}}}}
	if got, ok, err := HoursFromConfig(cfg); !ok || err != nil || len(got["mon"]) != 1 {
		t.Errorf("config hours: %v %v %v", got, ok, err)
	}
}

// Lombardi's hours (the owner, 2026-11-12): Mon–Thu 10:00–01:00, Fri–Sat
// 10:00–02:00, Sun 10:30–00:00 — every day closes past (or at) midnight.
func lombardisHours() OpeningHours {
	wk := []HoursRange{{Open: "10:00", Close: "01:00"}}
	we := []HoursRange{{Open: "10:00", Close: "02:00"}}
	return OpeningHours{"mon": wk, "tue": wk, "wed": wk, "thu": wk, "fri": we, "sat": we,
		"sun": {{Open: "10:30", Close: "00:00"}}}
}

func TestOvernightHoursLombardis(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	h := lombardisHours()
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, ny)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	// 2026-11-09 is a Monday … 2026-11-15 a Sunday.
	for _, c := range []struct {
		when string
		open bool
	}{
		{"2026-11-09 00:30", false}, // Sunday closed at midnight
		{"2026-11-09 09:59", false},
		{"2026-11-09 10:00", true},
		{"2026-11-10 00:59", true}, // Monday's period runs to 01:00 Tuesday
		{"2026-11-10 01:00", false},
		{"2026-11-14 01:59", true}, // Friday's runs to 02:00 Saturday
		{"2026-11-14 02:00", false},
		{"2026-11-15 01:59", true}, // Saturday's runs to 02:00 Sunday
		{"2026-11-15 10:29", false},
		{"2026-11-15 10:30", true},
		{"2026-11-15 23:59", true},
		{"2026-11-16 00:00", false},
	} {
		if got := h.IsOpen(at(c.when), ny, 0); got != c.open {
			t.Errorf("%s: open=%v, want %v", c.when, got, c.open)
		}
	}
}
