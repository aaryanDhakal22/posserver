package bankimport

import (
	"context"
	"slices"
	"testing"

	"quiccpos/main/internal/domain/bankdoc"
	"quiccpos/main/internal/domain/pricing"

	"github.com/google/uuid"
)

// Included toppings in the bank document: the item's count, its sizes'
// own counts and an option's never_included apply, export, re-apply as a
// no-op, and clear.
func TestIncludedToppingsDoc(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	svc := testService(f)
	d := renameDoc()
	it := &d.Menus[0].Categories[0].Items[0]
	it.IncludedToppings = ptr(5)
	it.Sizes = &[]bankdoc.ItemSize{{Size: "Small", PriceCents: 900, IncludedToppings: ptr(2)}, {Size: "Large", PriceCents: 1300}}
	d.Groups[0].Options[0].NeverIncluded = ptr(true)
	res, err := svc.Apply(ctx, uuid.Nil, uuid.Nil, d, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Changes, `option "Pepperoni" of "Toppings": never_included`) {
		t.Fatalf("changes: %q", res.Changes)
	}
	small := ids(f)["size:Small"].String()
	for _, x := range f.items {
		if x.Name != "Cheese" {
			if n, s := pricing.IncludedOf(x.Attributes); n != 0 || s != nil {
				t.Fatalf("%s: %d %v", x.Name, n, s)
			}
			continue
		}
		n, s := pricing.IncludedOf(x.Attributes)
		if n != 5 || len(s) != 1 || s[small] != 2 {
			t.Fatalf("cheese: %d %v (%v)", n, s, x.Attributes)
		}
	}
	doc, err := svc.Export(ctx, uuid.Nil, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	ex := doc.Menus[0].Categories[0].Items[0]
	if ex.IncludedToppings == nil || *ex.IncludedToppings != 5 || (*ex.Sizes)[0].IncludedToppings == nil || *(*ex.Sizes)[0].IncludedToppings != 2 ||
		(*ex.Sizes)[1].IncludedToppings != nil || doc.Menus[0].Categories[0].Items[1].IncludedToppings != nil {
		t.Fatalf("export: %+v %+v", ex, *ex.Sizes)
	}
	if o := doc.Groups[0].Options; o[0].NeverIncluded == nil || !*o[0].NeverIncluded || o[1].NeverIncluded != nil {
		t.Fatalf("export options: %+v", o)
	}
	if res, _ := svc.Apply(ctx, uuid.Nil, uuid.Nil, doc, Options{}); len(res.Changes) != 0 {
		t.Errorf("export re-applied changed: %q", res.Changes)
	}
	// Bad counts are the document's error.
	bad := renameDoc()
	bad.Menus[0].Categories[0].Items[0].IncludedToppings = ptr(-1)
	if _, err := svc.Apply(ctx, uuid.Nil, uuid.Nil, bad, Options{}); err == nil {
		t.Fatal("a negative count applied")
	}
	// 0 clears the item's count; sizes named without counts clear theirs;
	// false clears never_included.
	d = renameDoc()
	d.Menus[0].Categories[0].Items[0].IncludedToppings = ptr(0)
	d.Groups[0].Options[0].NeverIncluded = ptr(false)
	if _, err := svc.Apply(ctx, uuid.Nil, uuid.Nil, d, Options{}); err != nil {
		t.Fatal(err)
	}
	for _, x := range f.items {
		if n, s := pricing.IncludedOf(x.Attributes); n != 0 || s != nil {
			t.Fatalf("%s not cleared: %d %v", x.Name, n, s)
		}
	}
	for _, g := range f.groups {
		if g.Options[0].NeverIncluded {
			t.Error("never_included still on")
		}
	}
}
