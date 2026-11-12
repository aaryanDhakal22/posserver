package bankimport

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"quiccpos/main/internal/domain/bankdoc"
	"quiccpos/main/internal/domain/menuview"

	"github.com/google/uuid"
)

func ptr[T any](v T) *T { return &v }

// renameDoc is a small store: two sizes, a Toppings group, Main → Pizza →
// Cheese (sized, with toppings) + Soda, and a counter view with an entry and
// a layout that sells Cheese Large and picks Onion.
func renameDoc() *bankdoc.Document {
	return &bankdoc.Document{Format: 1,
		Sizes: []bankdoc.Size{{Name: "Small"}, {Name: "Large"}},
		Groups: []bankdoc.Group{{Name: "Toppings", MaxSelect: 3, Options: []bankdoc.Option{
			{Name: "Pepperoni", PriceCents: 100}, {Name: "Onion", PriceCents: 50}}}},
		Menus: []bankdoc.Menu{{Name: "Main", Categories: []bankdoc.Category{{Name: "Pizza", Items: []bankdoc.Item{
			{Name: "Cheese", Sizes: &[]bankdoc.ItemSize{{Size: "Small", PriceCents: 900}, {Size: "Large", PriceCents: 1300}},
				Groups: &[]string{"Toppings"}},
			{Name: "Soda", PriceCents: 199},
		}}}}},
		Views: []bankdoc.View{{Kind: "counter", Name: "Counter", Entries: []bankdoc.Entry{{Kind: "item", Items: []string{"Cheese"}, DisplayName: "Cheese!"}},
			Layout: &bankdoc.Layout{TabStrip: menuview.Grid{Rows: 1, Cols: 1},
				Tabs: []menuview.TabOf[bankdoc.LayoutAction]{{Name: "Pizza", Grid: menuview.Grid{Rows: 1, Cols: 2}, Pages: []menuview.Page{{Name: "P"}},
					Buttons: []menuview.ButtonOf[bankdoc.LayoutAction]{
						{Row: 0, Col: 0, Action: bankdoc.LayoutAction{Type: "item", Item: "Cheese", Size: "Large"}},
						{Row: 0, Col: 1, Action: bankdoc.LayoutAction{Type: "category", Category: "Pizza"}}}}},
				Screens: []menuview.ScreenOf[bankdoc.LayoutAction]{{Name: "Tops", Grid: menuview.Grid{Rows: 1, Cols: 1}, Pages: []menuview.Page{{Name: "P"}},
					Buttons: []menuview.ButtonOf[bankdoc.LayoutAction]{{Action: bankdoc.LayoutAction{Type: "option", Group: "Toppings", Option: "Onion"}}}}},
				Flows: map[string][]bankdoc.LayoutStep{"Cheese": {{Screen: "Tops"}}}}}},
	}
}

// renamed is renameDoc with every kind renamed through `was` (and every
// reference using the new names).
func renamed() *bankdoc.Document {
	d := renameDoc()
	d.Sizes[1] = bankdoc.Size{Name: "Large 16", Was: []string{"large"}}
	d.Groups[0].Name, d.Groups[0].Was = "Pizza Toppings", []string{"Toppings"}
	d.Groups[0].Options[1] = bankdoc.Option{Name: "Onions", PriceCents: 50, Was: []string{" onion "}}
	d.Menus[0].Name, d.Menus[0].Was = "Counter Menu", []string{"Main"}
	c := &d.Menus[0].Categories[0]
	c.Name, c.Was = "Pizzas", []string{"Pizza"}
	it := &c.Items[0]
	it.Name, it.Was = "Cheese Pizza", []string{"Plain", "Cheese"}
	it.Sizes = &[]bankdoc.ItemSize{{Size: "Small", PriceCents: 900}, {Size: "Large 16", PriceCents: 1400}}
	it.Groups = &[]string{"Pizza Toppings"}
	v := &d.Views[0]
	v.Entries[0].Items = []string{"Cheese Pizza"}
	l := v.Layout
	l.Tabs[0].Buttons[0].Action = bankdoc.LayoutAction{Type: "item", Item: "Cheese Pizza", Size: "Large 16"}
	l.Tabs[0].Buttons[1].Action.Category = "Pizzas"
	l.Screens[0].Buttons[0].Action = bankdoc.LayoutAction{Type: "option", Group: "Pizza Toppings", Option: "Onions"}
	l.Flows = map[string][]bankdoc.LayoutStep{"Cheese Pizza": {{Screen: "Tops"}}}
	return d
}

// ids are the store's row ids by kind and current name.
func ids(f *fakeStore) map[string]uuid.UUID {
	out := map[string]uuid.UUID{}
	for _, z := range f.sizes {
		out["size:"+z.Name] = z.ID
	}
	for _, g := range f.groups {
		out["group:"+g.Name] = g.ID
		for _, o := range g.Options {
			out["option:"+o.Name] = o.ID
		}
	}
	for _, m := range f.menus {
		out["menu:"+m.Name] = m.ID
	}
	for _, c := range f.cats {
		out["category:"+c.Name] = c.ID
	}
	for _, it := range f.items {
		out["item:"+it.Name] = it.ID
	}
	return out
}

func TestRenameInPlace(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	svc := testService(f)
	if _, err := svc.Apply(ctx, uuid.Nil, uuid.Nil, renameDoc(), Options{}); err != nil {
		t.Fatal(err)
	}
	before := ids(f)

	dry, err := svc.Apply(ctx, uuid.Nil, uuid.Nil, renamed(), Options{DryRun: true, Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`size "Large" renamed to "Large 16"`, `group "Toppings" renamed to "Pizza Toppings"`,
		`option "Onion" of "Pizza Toppings" renamed to "Onions"`, `menu "Main" renamed to "Counter Menu"`,
		`category "Pizza" renamed to "Pizzas"`, `item "Cheese" renamed to "Cheese Pizza"`, `item "Cheese Pizza": sizes`} {
		if !slices.Contains(dry.Changes, want) {
			t.Errorf("dry run changes lack %q: %q", want, dry.Changes)
		}
	}
	for k, c := range dry.Counts {
		if c.Deleted != 0 && k != "entries" {
			t.Errorf("%s: %d deleted (renamed rows must not be pruned)", k, c.Deleted)
		}
	}
	if got := ids(f); got["item:Cheese"] != before["item:Cheese"] || got["item:Cheese Pizza"] != uuid.Nil {
		t.Fatalf("the dry run wrote: %v", got)
	}

	res, err := svc.Apply(ctx, uuid.Nil, uuid.Nil, renamed(), Options{Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	after := ids(f)
	for old, nu := range map[string]string{"size:Large": "size:Large 16", "group:Toppings": "group:Pizza Toppings",
		"option:Onion": "option:Onions", "menu:Main": "menu:Counter Menu", "category:Pizza": "category:Pizzas", "item:Cheese": "item:Cheese Pizza"} {
		if after[nu] == uuid.Nil || after[nu] != before[old] {
			t.Errorf("%s → %s: id %s, was %s", old, nu, after[nu], before[old])
		}
		if _, still := after[old]; still {
			t.Errorf("%s is still there", old)
		}
	}
	if res.Counts["items"].Updated != 1 || res.Counts["items"].Created != 0 || res.Counts["sizes"].Updated != 1 {
		t.Errorf("counts: items %+v sizes %+v", res.Counts["items"], res.Counts["sizes"])
	}
	// The layout and entries still point at the same rows.
	var view menuview.View
	for _, v := range f.views {
		view = v
	}
	l := f.layouts[view.ID]
	if l == nil || *l.Tabs[0].Buttons[0].Action.ItemID != before["item:Cheese"] || *l.Tabs[0].Buttons[0].Action.SizeID != before["size:Large"] ||
		*l.Screens[0].Buttons[0].Action.OptionID != before["option:Onion"] || l.Flows[before["item:Cheese"].String()] == nil {
		t.Errorf("layout after the rename: %+v", l)
	}
	if e := f.entries[view.ID]; len(e) != 1 || e[0].Anchor.ItemIDs[0] != before["item:Cheese"] {
		t.Errorf("entries after the rename: %+v", e)
	}

	// Re-applying the same document changes nothing.
	again, err := svc.Apply(ctx, uuid.Nil, uuid.Nil, renamed(), Options{Prune: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Changes) != 0 {
		t.Errorf("re-apply changed: %q", again.Changes)
	}
	// Export never writes was.
	doc, err := svc.Export(ctx, uuid.Nil, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Sizes[1].Was != nil || doc.Groups[0].Was != nil || doc.Groups[0].Options[1].Was != nil || doc.Menus[0].Was != nil ||
		doc.Menus[0].Categories[0].Was != nil || doc.Menus[0].Categories[0].Items[0].Was != nil {
		t.Errorf("export wrote was: %+v", doc)
	}
}

func TestRenameConflicts(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(d *bankdoc.Document)
		path  string
		inMsg string
	}{
		{"was + another row's own name", func(d *bankdoc.Document) {
			c := &d.Menus[0].Categories[0]
			c.Items = append(c.Items, bankdoc.Item{Name: "Margherita", Was: []string{"Cheese"}})
		}, "menus[0].categories[0].items[2].was", `item "Cheese" is also matched by menus[0].categories[0].items[0]`},
		{"two was", func(d *bankdoc.Document) {
			c := &d.Menus[0].Categories[0]
			c.Items[1].Name, c.Items[1].Was = "Pop", []string{"Soda"}
			c.Items = append(c.Items, bankdoc.Item{Name: "Soft Drink", Was: []string{"soda"}})
		}, "menus[0].categories[0].items[2].was", "also matched by menus[0].categories[0].items[1]"},
		{"was names two rows", func(d *bankdoc.Document) {
			c := &d.Menus[0].Categories[0]
			c.Items[0].Name, c.Items[0].Was = "Cheese Pizza", []string{"Cheese", "Soda"}
		}, "menus[0].categories[0].items[0].was", "two different items"},
		{"wrong kind", func(d *bankdoc.Document) {
			c := &d.Menus[0].Categories[0]
			c.Items = append(c.Items, bankdoc.Item{Name: "Margherita", Was: []string{"Pizza"}})
		}, "menus[0].categories[0].items[2].was[0]", `was "Pizza" names a category, not an item`},
		{"option of another group", func(d *bankdoc.Document) {
			d.Groups = append(d.Groups, bankdoc.Group{Name: "Sauce", Options: []bankdoc.Option{{Name: "Red", Was: []string{"Onion"}}}})
		}, "groups[1].options[0].was[0]", `names an option of group "Toppings"`},
		{"size claimed twice", func(d *bankdoc.Document) {
			d.Sizes = append(d.Sizes, bankdoc.Size{Name: "Big", Was: []string{"Large"}})
		}, "sizes[2].was", `size "Large" is also matched by sizes[1]`},
		{"empty was", func(d *bankdoc.Document) { d.Groups[0].Was = []string{" "} }, "groups[0].was[0]", "can't be empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFakeStore()
			svc := testService(f)
			if _, err := svc.Apply(ctx, uuid.Nil, uuid.Nil, renameDoc(), Options{}); err != nil {
				t.Fatal(err)
			}
			d := renameDoc()
			c.edit(d)
			_, err := svc.Apply(ctx, uuid.Nil, uuid.Nil, d, Options{DryRun: true})
			var e *BadDocError
			if !errors.As(err, &e) || !errors.Is(err, ErrBadDocument) {
				t.Fatalf("want a bad document, got %v", err)
			}
			if e.Path != c.path || !strings.Contains(e.Msg, c.inMsg) {
				t.Errorf("got %s: %s; want %s ~ %q", e.Path, e.Msg, c.path, c.inMsg)
			}
		})
	}
}

func TestRenameDuplicateStoreRows(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	svc := testService(f)
	if _, err := svc.Apply(ctx, uuid.Nil, uuid.Nil, renameDoc(), Options{}); err != nil {
		t.Fatal(err)
	}
	// Two store items named "Soda" (the editor allows it): a was naming them is refused.
	dup := f.items[1]
	dup.ID = f.id()
	f.items = append(f.items, dup)
	d := renameDoc()
	d.Menus[0].Categories[0].Items[1] = bankdoc.Item{Name: "Pop", Was: []string{"Soda"}}
	_, err := svc.Apply(ctx, uuid.Nil, uuid.Nil, d, Options{DryRun: true})
	var e *BadDocError
	if !errors.As(err, &e) || e.Path != "menus[0].categories[0].items[1].was[0]" || !strings.Contains(e.Msg, "matches 2 items") {
		t.Fatalf("got %v", err)
	}
}

func TestShowOnDriverTicketDoc(t *testing.T) {
	ctx := context.Background()
	f := newFakeStore()
	svc := testService(f)
	d := renameDoc()
	d.Groups[0].Options[0].ShowOnDriverTicket = ptr(true)
	if _, err := svc.Apply(ctx, uuid.Nil, uuid.Nil, d, Options{}); err != nil {
		t.Fatal(err)
	}
	doc, err := svc.Export(ctx, uuid.Nil, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	if o := doc.Groups[0].Options; o[0].ShowOnDriverTicket == nil || !*o[0].ShowOnDriverTicket || o[1].ShowOnDriverTicket != nil {
		t.Fatalf("export: %+v", o)
	}
	// Absent keeps; the export applies as a no-op; false clears.
	if res, _ := svc.Apply(ctx, uuid.Nil, uuid.Nil, renameDoc(), Options{}); len(res.Changes) != 0 {
		t.Errorf("absent changed: %q", res.Changes)
	}
	if res, _ := svc.Apply(ctx, uuid.Nil, uuid.Nil, doc, Options{}); len(res.Changes) != 0 {
		t.Errorf("export re-applied changed: %q", res.Changes)
	}
	d.Groups[0].Options[0].ShowOnDriverTicket = ptr(false)
	res, err := svc.Apply(ctx, uuid.Nil, uuid.Nil, d, Options{})
	if err != nil || !slices.Contains(res.Changes, `option "Pepperoni" of "Toppings": show_on_driver_ticket`) {
		t.Fatalf("clear: %v %q", err, res.Changes)
	}
	for _, g := range f.groups {
		if g.Options[0].ShowOnDriverTicket {
			t.Error("still on")
		}
	}
}
