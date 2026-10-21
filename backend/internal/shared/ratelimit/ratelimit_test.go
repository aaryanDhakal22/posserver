package ratelimit

import (
	"testing"
	"time"
)

func TestWindowAllowAddReset(t *testing.T) {
	w := New(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !w.Allow("ip") {
			t.Fatalf("attempt %d refused", i)
		}
		w.Add("ip")
	}
	if w.Allow("ip") {
		t.Fatal("4th attempt allowed after 3 hits")
	}
	if !w.Allow("other") {
		t.Fatal("other key throttled")
	}
	w.Reset("ip")
	if !w.Allow("ip") {
		t.Fatal("reset did not clear")
	}
}

func TestWindowSlidesAndRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	w := New(2, time.Hour)
	w.SetClock(func() time.Time { return now })

	w.Add("k")
	now = now.Add(10 * time.Minute)
	w.Add("k")
	if w.Allow("k") {
		t.Fatal("third hit within the hour allowed")
	}
	if got := w.RetryAfter("k"); got != 50*time.Minute {
		t.Fatalf("RetryAfter = %v, want 50m (oldest hit ages out)", got)
	}
	now = now.Add(50 * time.Minute) // the first hit is now an hour old
	if !w.Allow("k") {
		t.Fatal("window did not slide")
	}
	if got := w.RetryAfter("k"); got != 0 {
		t.Fatalf("RetryAfter with room = %v, want 0", got)
	}
}

func TestWindowAllowNSharedKey(t *testing.T) {
	w := New(1, time.Minute)
	for i := 0; i < 5; i++ {
		if !w.AllowN("*", 5) {
			t.Fatalf("global hit %d refused", i)
		}
		w.Add("*")
	}
	if w.AllowN("*", 5) {
		t.Fatal("global ceiling not applied")
	}
	if w.RetryAfterN("*", 5) <= 0 {
		t.Fatal("RetryAfterN should be positive when full")
	}
}
