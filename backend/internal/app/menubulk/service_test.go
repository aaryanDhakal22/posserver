package menubulk

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func ptr[T any](v T) *T { return &v }

type fakeRepo struct{ got []Resolved }

func (f *fakeRepo) ApplyItemChanges(_ context.Context, _, _ uuid.UUID, cs []Resolved) error {
	f.got = cs
	return nil
}

func TestCheck(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	size := uuid.New()
	cases := []struct {
		name string
		in   []Change
		want error
	}{
		{"empty", nil, ErrBadBody},
		{"no item", []Change{{PriceCents: ptr(int64(100))}}, ErrBadBody},
		{"twice", []Change{{ItemID: a, InStock: ptr(true)}, {ItemID: a, InStock: ptr(false)}}, ErrBadBody},
		{"nothing", []Change{{ItemID: a}}, ErrBadBody},
		{"negative", []Change{{ItemID: a, PriceCents: ptr(int64(-1))}}, ErrBadPrice},
		{"too dear", []Change{{ItemID: a, PriceCents: ptr(int64(MaxPriceCents + 1))}}, ErrBadPrice},
		{"bad size key", []Change{{ItemID: a, SizePrices: map[string]int64{"x": 100}}}, ErrBadBody},
		{"negative size", []Change{{ItemID: a, SizePrices: map[string]int64{size.String(): -5}}}, ErrBadPrice},
		{"bad printer", []Change{{ItemID: a, PrinterID: ptr("nope")}}, ErrBadBody},
		{"bad tax", []Change{{ItemID: a, TaxRateBps: ptr(10001)}}, ErrBadBody},
		{"ok", []Change{{ItemID: a, PrinterID: ptr("")}, {ItemID: b, SizePrices: map[string]int64{size.String(): 0}}}, nil},
	}
	for _, c := range cases {
		_, err := Check(c.in)
		if !errors.Is(err, c.want) && !(c.want == nil && err == nil) {
			t.Errorf("%s: got %v want %v", c.name, err, c.want)
		}
	}
	tooMany := make([]Change, MaxChanges+1)
	for i := range tooMany {
		tooMany[i] = Change{ItemID: uuid.New(), InStock: ptr(true)}
	}
	if _, err := Check(tooMany); !errors.Is(err, ErrBadBody) {
		t.Fatalf("too many: %v", err)
	}
}

func TestResolved(t *testing.T) {
	a, b, p, size := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f := &fakeRepo{}
	n, err := NewService(f).Apply(context.Background(), uuid.New(), uuid.New(), []Change{
		{ItemID: a, PrinterID: ptr("")},
		{ItemID: b, PrinterID: ptr(p.String()), SizePrices: map[string]int64{size.String(): 1299}},
	})
	if err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	if !f.got[0].SetPrinter || f.got[0].Printer != nil {
		t.Fatalf("clear printer: %+v", f.got[0])
	}
	if !f.got[1].SetPrinter || *f.got[1].Printer != p || f.got[1].Sizes[size] != 1299 {
		t.Fatalf("set printer: %+v", f.got[1])
	}
}
