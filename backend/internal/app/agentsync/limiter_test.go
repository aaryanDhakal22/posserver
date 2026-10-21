package agentsync

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	l := newLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !l.allow("ip") {
			t.Fatalf("attempt %d refused", i)
		}
		l.fail("ip")
	}
	if l.allow("ip") {
		t.Fatal("4th attempt allowed after 3 failures")
	}
	if !l.allow("other") {
		t.Fatal("other key throttled")
	}
	l.reset("ip")
	if !l.allow("ip") {
		t.Fatal("reset did not clear")
	}
}
