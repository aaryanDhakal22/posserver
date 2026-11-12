package notify

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type countN struct{ n int }

func (c *countN) Notify(context.Context, Notice) error { c.n++; return nil }

type fakePager struct{ sent []string }

func (f *fakePager) Enabled() bool { return true }
func (f *fakePager) SendAlso(_ context.Context, _ []string, title, _, _ string) error {
	f.sent = append(f.sent, title)
	return nil
}

func TestTestStoresKeepTestPagesOffTheOwnersPhone(t *testing.T) {
	real, test, broken := uuid.New(), uuid.New(), uuid.New()
	isTest := func(_ context.Context, id uuid.UUID) (bool, error) {
		if id == broken {
			return false, errors.New("db down")
		}
		return id == test, nil
	}
	for _, c := range []struct {
		mode              string
		realN, testN, dev int
	}{{"", 1, 0, 0}, {"log", 1, 0, 0}, {"pushover", 1, 1, 0}, {"device:quicc-tests", 1, 0, 1}, {"bogus", 1, 0, 0}} {
		mode, _ := ParseTestStoresMode(c.mode)
		inner, dev := &countN{}, &fakePager{}
		ts := TestStores{Inner: inner, IsTest: isTest, Mode: mode, Device: dev}
		_ = ts.Notify(context.Background(), Notice{LocationID: real, Title: "Money to check"})
		if inner.n != c.realN {
			t.Fatalf("%q: a real store's notice reached Inner %d times", c.mode, inner.n)
		}
		_ = ts.Notify(context.Background(), Notice{LocationID: test, Title: "Money to check"})
		if inner.n != c.realN+c.testN || len(dev.sent) != c.dev {
			t.Fatalf("%q: test store: inner %d, device %v", c.mode, inner.n, dev.sent)
		}
		if c.dev == 1 && dev.sent[0] != "[test store] Money to check" {
			t.Fatalf("device title %q", dev.sent[0])
		}
		// A failed lookup still pages: a real store must never lose one.
		before := inner.n
		_ = ts.Notify(context.Background(), Notice{LocationID: broken})
		if inner.n != before+1 {
			t.Fatalf("%q: a failed lookup dropped the page", c.mode)
		}
	}
	if _, ok := ParseTestStoresMode("device:"); ok {
		t.Fatal("device: with no name is not a mode")
	}
}
