package agentsync

import (
	"errors"
	"testing"
	"time"

	"quiccpos/main/internal/domain/tenancy"
)

var t0 = time.Date(2026, 11, 12, 20, 40, 0, 0, time.UTC)

func mins(n int) time.Duration { return time.Duration(n) * time.Minute }

// tick runs decide and, for a send, result with err.
func tick(o *ownerOffline, at time.Time, connected, open bool, s tenancy.OwnerAlertSettings, err error) ownerAction {
	a, _ := o.decide(at, connected, open, s)
	if a != ownerNone {
		o.result(a, at, err)
	}
	return a
}

func TestOwnerOfflineOncePerOutagePlusRecovery(t *testing.T) {
	s := tenancy.OwnerAlertDefaults()
	o := newOwnerOffline()
	if a := tick(o, t0, false, true, s, nil); a != ownerNone {
		t.Fatalf("first seen offline = %v", a)
	}
	if a := tick(o, t0.Add(mins(4)), false, true, s, nil); a != ownerNone {
		t.Fatalf("a blip under 5 min = %v", a)
	}
	if a := tick(o, t0.Add(mins(5)), false, true, s, nil); a != ownerSendOffline {
		t.Fatalf("5 min = %v", a)
	}
	for i := 6; i < 30; i++ {
		if a := tick(o, t0.Add(mins(i)), false, true, s, nil); a != ownerNone {
			t.Fatalf("once per outage, got %v at %d min", a, i)
		}
	}
	if a := tick(o, t0.Add(mins(30)), true, true, s, nil); a != ownerSendOnline {
		t.Fatalf("back = %v", a)
	}
	if a := tick(o, t0.Add(mins(31)), true, true, s, nil); a != ownerNone {
		t.Fatalf("back only once, got %v", a)
	}
}

func TestOwnerOfflineShortOutageNeverTold(t *testing.T) {
	s := tenancy.OwnerAlertDefaults()
	o := newOwnerOffline()
	tick(o, t0, false, true, s, nil)
	tick(o, t0.Add(mins(3)), false, true, s, nil)
	if a := tick(o, t0.Add(mins(4)), true, true, s, nil); a != ownerNone {
		t.Fatalf("a short outage has no recovery message, got %v", a)
	}
	// The next outage starts its own clock.
	tick(o, t0.Add(mins(10)), false, true, s, nil)
	if a := tick(o, t0.Add(mins(14)), false, true, s, nil); a != ownerNone {
		t.Fatalf("4 min into the new outage = %v", a)
	}
}

func TestOwnerOfflineSettingFromTheLocation(t *testing.T) {
	s := tenancy.OwnerAlertSettingsFrom(tenancy.Config{tenancy.OwnerAlertOfflineMinutesKey: float64(10)})
	o := newOwnerOffline()
	tick(o, t0, false, true, s, nil)
	if a := tick(o, t0.Add(mins(9)), false, true, s, nil); a != ownerNone {
		t.Fatalf("9 of 10 min = %v", a)
	}
	if a := tick(o, t0.Add(mins(10)), false, true, s, nil); a != ownerSendOffline {
		t.Fatalf("10 min = %v", a)
	}
}

func TestOwnerOfflineOpeningHours(t *testing.T) {
	s := tenancy.OwnerAlertDefaults()
	o := newOwnerOffline()
	tick(o, t0, false, false, s, nil)
	if a, why := o.decide(t0.Add(mins(60)), false, false, s); a != ownerNone || why != "closed" {
		t.Fatalf("closed = %v %q", a, why)
	}
	// Still out when the store opens: told then.
	if a := tick(o, t0.Add(mins(61)), false, true, s, nil); a != ownerSendOffline {
		t.Fatalf("at opening = %v", a)
	}
	// Back at night: no recovery message (as on the Pi), and no debt left.
	if a, why := o.decide(t0.Add(mins(90)), true, false, s); a != ownerNone || why != "closed" {
		t.Fatalf("back while closed = %v %q", a, why)
	}
	if a := tick(o, t0.Add(mins(120)), true, true, s, nil); a != ownerNone {
		t.Fatalf("no late recovery, got %v", a)
	}
	// An outage that ended at night is never told.
	p := newOwnerOffline()
	tick(p, t0, false, false, s, nil)
	tick(p, t0.Add(mins(30)), false, false, s, nil)
	tick(p, t0.Add(mins(40)), true, false, s, nil)
	if a := tick(p, t0.Add(mins(50)), true, true, s, nil); a != ownerNone {
		t.Fatalf("a night outage = %v", a)
	}
}

func TestOwnerOfflineCooldown(t *testing.T) {
	s := tenancy.OwnerAlertDefaults()
	o := newOwnerOffline()
	tick(o, t0, false, true, s, nil)
	tick(o, t0.Add(mins(5)), false, true, s, nil)  // offline sent at +5
	tick(o, t0.Add(mins(10)), true, true, s, nil)  // back sent at +10
	tick(o, t0.Add(mins(12)), false, true, s, nil) // out again
	if a, why := o.decide(t0.Add(mins(20)), false, true, s); a != ownerNone || why != "cooldown" {
		t.Fatalf("second outage inside the cooldown = %v %q", a, why)
	}
	// Still out when the cooldown ends (30 min after +5): told then.
	if a := tick(o, t0.Add(mins(35)), false, true, s, nil); a != ownerSendOffline {
		t.Fatalf("after the cooldown = %v", a)
	}
}

func TestOwnerOfflineCaps(t *testing.T) {
	s := tenancy.OwnerAlertDefaults()
	s.Cooldown = 0 // only the cap counts here
	s.MaxPerHour = 2
	o := newOwnerOffline()
	tick(o, t0, false, true, s, nil)
	tick(o, t0.Add(mins(5)), false, true, s, nil) // 1
	tick(o, t0.Add(mins(6)), true, true, s, nil)  // 2
	tick(o, t0.Add(mins(7)), false, true, s, nil)
	if a, why := o.decide(t0.Add(mins(12)), false, true, s); a != ownerNone || why != "capped" {
		t.Fatalf("over the hourly cap = %v %q", a, why)
	}
	if a := tick(o, t0.Add(mins(13)), true, true, s, nil); a != ownerNone {
		t.Fatalf("no recovery for an outage the cap held, got %v", a)
	}
}

func TestOwnerOfflineSwitchedOff(t *testing.T) {
	s := tenancy.OwnerAlertSettingsFrom(tenancy.Config{tenancy.OwnerAlertsKey: false})
	o := newOwnerOffline()
	tick(o, t0, false, true, s, nil)
	if a, why := o.decide(t0.Add(mins(10)), false, true, s); a != ownerNone || why != "off" {
		t.Fatalf("owner_alerts false = %v %q", a, why)
	}
}

func TestOwnerOfflineFailedSendIsOfferedAgain(t *testing.T) {
	s := tenancy.OwnerAlertDefaults()
	o := newOwnerOffline()
	tick(o, t0, false, true, s, nil)
	if a := tick(o, t0.Add(mins(5)), false, true, s, errors.New("pushover down")); a != ownerSendOffline {
		t.Fatalf("first try = %v", a)
	}
	if a := tick(o, t0.Add(mins(5)+30*time.Second), false, true, s, nil); a != ownerSendOffline {
		t.Fatalf("offered again = %v", a)
	}
	// A failed "back online" is offered again too.
	if a := tick(o, t0.Add(mins(8)), true, true, s, errors.New("down")); a != ownerSendOnline {
		t.Fatalf("back, first try = %v", a)
	}
	if a := tick(o, t0.Add(mins(9)), true, true, s, nil); a != ownerSendOnline {
		t.Fatalf("back, again = %v", a)
	}
}

func TestOwnerOfflineNobodyToTell(t *testing.T) {
	s := tenancy.OwnerAlertDefaults()
	o := newOwnerOffline()
	tick(o, t0, false, true, s, nil)
	if a := tick(o, t0.Add(mins(5)), false, true, s, errNobody); a != ownerSendOffline {
		t.Fatalf("offered = %v", a)
	}
	if a := tick(o, t0.Add(mins(6)), false, true, s, nil); a != ownerNone {
		t.Fatalf("nobody = dealt with, got %v", a)
	}
	if a := tick(o, t0.Add(mins(7)), true, true, s, nil); a != ownerNone {
		t.Fatalf("nobody was told, so no recovery: %v", a)
	}
}

func TestOwnerOfflineInFlight(t *testing.T) {
	s := tenancy.OwnerAlertDefaults()
	o := newOwnerOffline()
	o.decide(t0, false, true, s)
	if a, _ := o.decide(t0.Add(mins(5)), false, true, s); a != ownerSendOffline {
		t.Fatalf("send = %v", a)
	}
	if a, _ := o.decide(t0.Add(mins(5)+30*time.Second), false, true, s); a != ownerNone {
		t.Fatalf("one send at a time, got %v", a)
	}
}

func TestOwnerOfflineWording(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	at := time.Date(2026, 11, 12, 20, 42, 10, 0, ny)
	want := "Lombardi's tablet system went offline at 8:42 PM. Check the store's internet and power. Online orders will wait until it's back."
	if got := ownerOfflineText("Lombardi's", at.UTC(), ny); got != want {
		t.Fatalf("offline =\n%s\nwant\n%s", got, want)
	}
	if got := ownerOnlineText("Lombardi's"); got != "Lombardi's is back online. Orders that waited are printing now." {
		t.Fatalf("online = %s", got)
	}
	if got := ownerOnlineText(" "); got != "The store is back online. Orders that waited are printing now." {
		t.Fatalf("no name = %s", got)
	}
}
