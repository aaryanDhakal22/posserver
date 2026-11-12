package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

func TestLoginGuardPerUsername(t *testing.T) {
	g := NewLoginGuard()
	for i := 0; i < LoginUserMax; i++ {
		if ok, _ := g.Check("Lombardispizza", "203.0.113.1"); !ok {
			t.Fatalf("attempt %d refused", i+1)
		}
		g.Fail(" lombardispizza ", "203.0.113.1")
	}
	ok, wait := g.Check("lombardispizza", "198.51.100.2") // from anywhere
	if ok || wait <= 0 || wait > LoginWindow {
		t.Fatalf("after %d failures: ok=%v wait=%v", LoginUserMax, ok, wait)
	}
	if ok, _ := g.Check("someoneelse", "198.51.100.2"); !ok {
		t.Fatal("another username from another address is throttled")
	}
	g.Success("lombardispizza")
	if ok, _ := g.Check("lombardispizza", "198.51.100.2"); !ok {
		t.Fatal("a success did not clear the username")
	}
}

// One caller spraying usernames is stopped by its own address, and nobody else is.
func TestLoginGuardPerAddress(t *testing.T) {
	t.Setenv("LOGIN_IP_MAX", "")
	g := NewLoginGuard()
	const sprayer = "203.0.113.66"
	for i := 0; i < LoginIPMax; i++ {
		user := fmt.Sprintf("store%d", i)
		if ok, _ := g.Check(user, sprayer); !ok {
			t.Fatalf("spray attempt %d refused early", i+1)
		}
		g.Fail(user, sprayer)
	}
	if ok, wait := g.Check("freshname", sprayer); ok || wait <= 0 {
		t.Fatalf("the sprayer's address is not throttled after %d failures (wait %v)", LoginIPMax, wait)
	}
	if ok, _ := g.Check("freshname", "198.51.100.2"); !ok {
		t.Fatal("another address is throttled by the sprayer's failures")
	}
	// A success elsewhere doesn't hand the sprayer a fresh allowance.
	g.Success("store1")
	if ok, _ := g.Check("freshname", sprayer); ok {
		t.Fatal("a success reset the address")
	}
}

// H2: 30 failures on other usernames used to lock every manager out. Now
// even a shut global valve lets a login through when neither its username
// nor its address has failed.
func TestLoginGuardOthersCannotLockAManagerOut(t *testing.T) {
	t.Setenv("LOGIN_GLOBAL_MAX", "")
	t.Setenv("LOGIN_IP_MAX", "")
	g := NewLoginGuard()
	_, _, global := g.Limits()
	if global < 600 {
		t.Fatalf("global valve default = %d, want >= 600", global)
	}
	// The old attack: 30 failures from one caller.
	for i := 0; i < 30; i++ {
		g.Fail(fmt.Sprintf("x%d", i), "203.0.113.66")
	}
	if ok, _ := g.Check("lombardispizza", "198.51.100.2"); !ok {
		t.Fatal("30 failures by one caller lock a manager out (the H2 lockout)")
	}
	// A spray from many addresses that shuts the valve.
	for i := 0; i < global; i++ {
		g.Fail(fmt.Sprintf("y%d", i), fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255))
	}
	if ok, _ := g.Check("lombardispizza", "198.51.100.2"); !ok {
		t.Fatal("a shut valve blocks a username and address with no failures")
	}
	// While it is shut, one failure of your own is enough to wait...
	g.Fail("lombardispizza", "198.51.100.2")
	ok, wait := g.Check("lombardispizza", "198.51.100.3")
	if ok || wait <= 0 {
		t.Fatalf("valve shut + own failure: ok=%v wait=%v, want throttled", ok, wait)
	}
	if ok, _ := g.Check("other", "198.51.100.2"); ok {
		t.Fatal("valve shut + the address's own failure: want throttled")
	}
	// ...and a sprayer gets no second guess at a username.
	if ok, _ := g.Check("y1", "192.0.2.200"); ok {
		t.Fatal("valve shut: a username that already failed got another guess")
	}
}

func TestLoginGuardUnknownAddressAndWindow(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	g := NewLoginGuard()
	g.SetClock(func() time.Time { return now })
	for i := 0; i < LoginUserMax; i++ {
		g.Fail("u", "") // no address known: the username still counts
	}
	if ok, _ := g.Check("u", ""); ok {
		t.Fatal("username not throttled without an address")
	}
	if ok, _ := g.Check("v", ""); !ok {
		t.Fatal("an empty address is throttled as if it were one caller")
	}
	now = now.Add(LoginWindow)
	if ok, _ := g.Check("u", ""); !ok {
		t.Fatal("the window did not slide")
	}
}

func TestLoginGuardEnv(t *testing.T) {
	t.Setenv("LOGIN_IP_MAX", "3")
	t.Setenv("LOGIN_GLOBAL_MAX", "7")
	g := NewLoginGuard()
	if u, ip, gl := g.Limits(); u != LoginUserMax || ip != 3 || gl != 7 {
		t.Fatalf("limits = %d %d %d", u, ip, gl)
	}
}
