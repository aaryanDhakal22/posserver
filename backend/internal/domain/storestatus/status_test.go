package storestatus

import (
	"testing"
	"time"

	"quiccpos/main/internal/domain/tenancy"
)

var ny, _ = time.LoadLocation("America/New_York")

// at is a New York wall-clock time on 2026-11-02 (a Monday) + days.
func at(days, h, m int) time.Time { return time.Date(2026, 11, 2+days, h, m, 0, 0, ny) }

// Mon–Thu 11:00–22:00, Fri 11:00–01:00 (past midnight), Sun closed.
func sched() Schedule {
	r := []tenancy.HoursRange{{Open: "11:00", Close: "22:00"}}
	return Schedule{Loc: ny, Weekly: tenancy.OpeningHours{
		"mon": r, "tue": r, "wed": r, "thu": r, "fri": {{Open: "11:00", Close: "01:00"}}, "sat": r,
	}, Exceptions: map[string]Exception{}}
}

func settings() Settings {
	return Settings{PickupEnabled: true, DeliveryEnabled: true, PickupETAMinutes: 20, DeliveryETAMinutes: 45, ScheduleDays: 7}
}

func TestCompute(t *testing.T) {
	s := Compute(at(0, 12, 0), sched(), settings(), 8047)
	if s.State != StateOpen || !s.Accepting || s.NextOpenAt != nil || !s.ClosesAt.Equal(at(0, 22, 0)) {
		t.Errorf("open: %+v", s)
	}
	if s.Today.Date != "2026-11-02" || len(s.Today.Hours) != 1 || s.OrderTypes.Delivery.RadiusM != 8047 {
		t.Errorf("today/types: %+v", s)
	}
	s = Compute(at(0, 23, 0), sched(), settings(), 0)
	if s.State != StateClosed || s.Accepting || !s.NextOpenAt.Equal(at(1, 11, 0)) {
		t.Errorf("closed: %+v next %v", s, s.NextOpenAt)
	}
	// Friday's hours run to 01:00 Saturday.
	s = Compute(at(5, 0, 30), sched(), settings(), 0)
	if s.State != StateOpen || !s.ClosesAt.Equal(at(5, 1, 0)) {
		t.Errorf("overnight: %+v", s)
	}
	// Sunday closed → Monday.
	s = Compute(at(6, 12, 0), sched(), settings(), 0)
	if s.State != StateClosed || !s.Today.Closed || !s.NextOpenAt.Equal(at(7, 11, 0)) {
		t.Errorf("sunday: %+v", s)
	}
	// Both types off: open but not accepting, no next-open time.
	st := settings()
	st.PickupEnabled, st.DeliveryEnabled = false, false
	s = Compute(at(0, 12, 0), sched(), st, 0)
	if s.State != StateOpen || s.Accepting || s.NextOpenAt != nil {
		t.Errorf("types off: %+v", s)
	}
}

func TestPause(t *testing.T) {
	now := at(0, 12, 0)
	sc := sched()
	st := settings()
	until, _ := sc.PauseUntil(now, "30m")
	st.PausedAt, st.PausedUntil, st.PauseNote = &now, until, "Backed up"
	s := Compute(now.Add(10*time.Minute), sc, st, 0)
	if s.State != StatePaused || s.Accepting || s.Pause.Note != "Backed up" || !s.NextOpenAt.Equal(at(0, 12, 30)) {
		t.Errorf("paused: %+v", s)
	}
	if s = Compute(now.Add(31*time.Minute), sc, st, 0); s.State != StateOpen || s.Pause != nil {
		t.Errorf("pause over: %+v", s)
	}
	st.PausedUntil = nil
	if s = Compute(now.Add(5*time.Hour), sc, st, 0); s.State != StatePaused || s.NextOpenAt != nil {
		t.Errorf("indefinite: %+v", s)
	}
	// A pause ending after closing → the next opening.
	late, _ := sc.PauseUntil(at(0, 21, 30), "60m")
	st.PausedAt, st.PausedUntil = &now, late
	if s = Compute(at(0, 21, 40), sc, st, 0); !s.NextOpenAt.Equal(at(1, 11, 0)) {
		t.Errorf("pause past closing: %v", s.NextOpenAt)
	}
	// "today": local midnight, or the end of an overnight period starting today.
	if u, _ := sc.PauseUntil(at(0, 12, 0), "today"); !u.Equal(at(1, 0, 0)) {
		t.Errorf("today: %v", u)
	}
	if u, _ := sc.PauseUntil(at(4, 12, 0), "today"); !u.Equal(at(5, 1, 0)) {
		t.Errorf("today, friday: %v", u)
	}
	if u, err := sc.PauseUntil(now, "indefinite"); u != nil || err != nil {
		t.Errorf("indefinite: %v %v", u, err)
	}
	if _, err := sc.PauseUntil(now, "2h"); err != ErrBadPause {
		t.Errorf("bad preset: %v", err)
	}
}

func TestExceptions(t *testing.T) {
	sc := sched()
	sc.Exceptions["2026-11-03"] = Exception{Date: "2026-11-03", Closed: true, Note: "Election day"}
	sc.Exceptions["2026-11-04"] = Exception{Date: "2026-11-04", Hours: []tenancy.HoursRange{{Open: "16:00", Close: "20:00"}}}
	s := Compute(at(1, 12, 0), sc, settings(), 0)
	if s.State != StateClosed || !s.Today.Closed || s.Today.Note != "Election day" || !s.NextOpenAt.Equal(at(2, 16, 0)) {
		t.Errorf("closed day: %+v next %v", s, s.NextOpenAt)
	}
	if s = Compute(at(2, 19, 0), sc, settings(), 0); !s.ClosesAt.Equal(at(2, 20, 0)) {
		t.Errorf("own hours: %+v", s)
	}
	bad := Exception{Date: "2026-13-01"}
	if bad.Validate() == nil {
		t.Error("bad date accepted")
	}
	noHours := Exception{Date: "2026-11-05"}
	if noHours.Validate() == nil {
		t.Error("open exception without hours accepted")
	}
	closed := Exception{Date: "2026-11-05", Closed: true, Hours: []tenancy.HoursRange{{Open: "x", Close: "y"}}}
	if closed.Validate() != nil || len(closed.Hours) != 0 {
		t.Error("closed exception drops its hours")
	}
}

func TestNoHoursIsAlwaysOpen(t *testing.T) {
	sc := Schedule{Loc: ny}
	s := Compute(at(0, 3, 0), sc, settings(), 0)
	if s.State != StateOpen || s.HoursSet || s.ClosesAt != nil {
		t.Errorf("no hours: %+v", s)
	}
}

func TestTimes(t *testing.T) {
	sc, st := sched(), settings()
	now := at(0, 12, 7)
	got, err := Times(now, sc, st, Pickup, "2026-11-02")
	// 12:07 + 20 min = 12:27 → 12:30, every 15 min to 22:00.
	if err != nil || !got[0].Equal(at(0, 12, 30)) || !got[len(got)-1].Equal(at(0, 22, 0)) || len(got) != 39 {
		t.Fatalf("today: %v %v (%d)", err, got[:1], len(got))
	}
	got, _ = Times(now, sc, st, Delivery, "2026-11-03")
	if !got[0].Equal(at(1, 11, 45)) {
		t.Errorf("tomorrow delivery: %v", got[0])
	}
	if got, _ = Times(now, sc, st, Pickup, "2026-11-08"); len(got) != 0 {
		t.Errorf("closed sunday: %v", got)
	}
	// A pause pushes today's first time; indefinite = none.
	until := at(0, 13, 0)
	st.PausedAt, st.PausedUntil = &now, &until
	if got, _ = Times(now, sc, st, Pickup, "2026-11-02"); !got[0].Equal(at(0, 13, 30)) {
		t.Errorf("paused: %v", got[0])
	}
	st.PausedUntil = nil
	if got, _ = Times(now, sc, st, Pickup, "2026-11-02"); len(got) != 0 {
		t.Errorf("indefinite pause: %v", got)
	}
	st = settings()
	for _, c := range []struct {
		kind, date string
		want       error
	}{{"dinein", "2026-11-02", ErrBadType}, {Pickup, "2026-11-01", ErrBadDate}, {Pickup, "2026-11-10", ErrBadDate}, {Pickup, "nope", ErrBadDate}} {
		if _, err := Times(now, sc, st, c.kind, c.date); err != c.want {
			t.Errorf("%s %s: %v", c.kind, c.date, err)
		}
	}
	st.DeliveryEnabled = false
	if _, err := Times(now, sc, st, Delivery, "2026-11-02"); err != ErrTypeOff {
		t.Errorf("type off: %v", err)
	}
	st.ScheduleDays = 0
	if _, err := Times(now, sc, st, Pickup, "2026-11-02"); err != ErrScheduleOff {
		t.Errorf("asap only: %v", err)
	}
}

func TestLastReset(t *testing.T) {
	if r := LastReset(at(0, 3, 0), ny, 4); !r.Equal(at(-1, 4, 0)) {
		t.Errorf("before the hour: %v", r)
	}
	if r := LastReset(at(0, 5, 0), ny, 4); !r.Equal(at(0, 4, 0)) {
		t.Errorf("after the hour: %v", r)
	}
}
