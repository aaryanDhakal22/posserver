package bankimport

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"quiccpos/main/internal/domain/bankdoc"
	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/menuview"

	"github.com/google/uuid"
)

func bid(n byte) uuid.UUID { return uuid.UUID{n} }

// layoutBank: Cheese Pizza (12/16) with Toppings, Soda (no sizes), Drinks.
func layoutBank() *menu.Bank {
	s12, s16 := bid(101), bid(102)
	top := menu.BankGroup{ModifierGroup: menu.ModifierGroup{ID: bid(201), Name: "Toppings"}, Options: []menu.BankOption{
		{ModifierOption: menu.ModifierOption{ID: bid(211), Name: "Pepperoni", InStock: true}},
		{ModifierOption: menu.ModifierOption{ID: bid(212), Name: "Onions", InStock: true}}}}
	return &menu.Bank{Sizes: []menu.Size{{ID: s12, Name: "12 Medium"}, {ID: s16, Name: "16 X-Large"}},
		Groups: map[uuid.UUID]menu.BankGroup{top.ID: top},
		Menus: []menu.BankMenu{{Menu: menu.Menu{ID: bid(2), Name: "Counter", Available: true}, Categories: []menu.BankCategory{
			{Category: menu.Category{ID: bid(10), Name: "Pizza", Available: true}, Items: []menu.BankItem{
				{Item: menu.Item{ID: bid(20), Name: "Cheese Pizza", InStock: true}, Sizes: []menu.ItemSize{{SizeID: s12}, {SizeID: s16}}}}},
			{Category: menu.Category{ID: bid(12), Name: "Drinks", Available: true}, Items: []menu.BankItem{
				{Item: menu.Item{ID: bid(40), Name: "Soda", InStock: true}}}},
		}}}}
}

func layoutApplier() *applier {
	bank := layoutBank()
	return &applier{bank: bank,
		sizes:  map[string]uuid.UUID{"12 medium": bid(101), "16 x-large": bid(102)},
		groups: map[string]uuid.UUID{"toppings": bid(201)},
		cats:   map[string]uuid.UUID{"pizza": bid(10), "drinks": bid(12)},
		items:  map[string]uuid.UUID{"cheese pizza": bid(20), "soda": bid(40)}}
}

func docLayout() bankdoc.Layout {
	return bankdoc.Layout{
		TabStrip: menuview.Grid{Rows: 1, Cols: 2},
		Tabs: []menuview.TabOf[bankdoc.LayoutAction]{{Name: "Pizza", Grid: menuview.Grid{Rows: 2, Cols: 2},
			Pages: []menuview.Page{{Name: "Page 1"}},
			Buttons: []menuview.ButtonOf[bankdoc.LayoutAction]{
				{Row: 0, Col: 0, Caption: "Med Cheese", Action: bankdoc.LayoutAction{Type: "item", Item: "cheese  PIZZA", Size: "12 Medium"}},
				{Row: 0, Col: 1, Action: bankdoc.LayoutAction{Type: "category", Category: "Drinks"}},
				{Row: 1, Col: 0, Action: bankdoc.LayoutAction{Type: "deal", Deal: "2 Medium Cheese"}},
				{Row: 1, Col: 1, Action: bankdoc.LayoutAction{Type: "function", Name: "open_item"}},
			}}},
		Screens: []menuview.ScreenOf[bankdoc.LayoutAction]{{Name: "Toppings", Grid: menuview.Grid{Rows: 1, Cols: 3},
			Pages: []menuview.Page{{Name: "Add Mods"}},
			Buttons: []menuview.ButtonOf[bankdoc.LayoutAction]{
				{Row: 0, Col: 0, Action: bankdoc.LayoutAction{Type: "option", Group: "Toppings", Option: "onions"}},
				{Row: 0, Col: 1, Action: bankdoc.LayoutAction{Type: "state", State: "second_add"}},
				{Row: 0, Col: 2, Action: bankdoc.LayoutAction{Type: "item_pick", Item: "Soda"}},
			}}},
		Flows:      map[string][]bankdoc.LayoutStep{"Cheese Pizza": {{Screen: "Toppings", Size: "16 X-Large", RequiredCount: 2, Forced: true}}},
		QuickNotes: []string{"Well Done", "Cut in squares"},
	}
}

func TestLayoutNamesToIDsAndBack(t *testing.T) {
	a := layoutApplier()
	deals := map[string]string{"2 medium cheese": "d1"}
	l, err := a.layoutIDs("views[0].layout", docLayout(), deals)
	if err != nil {
		t.Fatal(err)
	}
	b := l.Tabs[0].Buttons
	if *b[0].Action.ItemID != bid(20) || *b[0].Action.SizeID != bid(101) || *b[1].Action.CategoryID != bid(12) || b[2].Action.DealID != "d1" {
		t.Errorf("tab ids: %+v", b)
	}
	o := l.Screens[0].Buttons[0].Action
	if *o.GroupID != bid(201) || *o.OptionID != bid(212) {
		t.Errorf("option ids: %+v", o)
	}
	steps := l.Flows[bid(20).String()]
	if len(steps) != 1 || *steps[0].SizeID != bid(102) || steps[0].RequiredCount != 2 {
		t.Errorf("flows: %+v", l.Flows)
	}
	menuview.NormalizeLayout(&l)
	if ps := menuview.ValidateLayout(&l, menuview.LayoutRefs{Bank: a.bank, Deals: map[string]string{"d1": "2 Medium Cheese"}}); len(ps) > 0 {
		t.Fatalf("problems: %+v", ps)
	}
	// Export writes the bank's own names; applying that again gives the same ids.
	item := map[string]string{bid(20).String(): "Cheese Pizza", bid(40).String(): "Soda"}
	cat := map[string]string{bid(10).String(): "Pizza", bid(12).String(): "Drinks"}
	size := map[uuid.UUID]string{bid(101): "12 Medium", bid(102): "16 X-Large"}
	group := map[uuid.UUID]string{bid(201): "Toppings"}
	out := exportLayout(l, a.bank, item, cat, size, group, map[string]string{"d1": "2 Medium Cheese"})
	if got := out.Tabs[0].Buttons[0].Action; got.Item != "Cheese Pizza" || got.Size != "12 Medium" {
		t.Errorf("export: %+v", got)
	}
	if got := out.Screens[0].Buttons[0].Action; got.Group != "Toppings" || got.Option != "Onions" {
		t.Errorf("export option: %+v", got)
	}
	if _, ok := out.Flows["Cheese Pizza"]; !ok {
		t.Errorf("export flows: %+v", out.Flows)
	}
	if len(out.QuickNotes) != 2 || out.QuickNotes[1] != "Cut in squares" {
		t.Errorf("export quick notes: %+v", out.QuickNotes)
	}
	again, err := a.layoutIDs("x", out, deals)
	if err != nil {
		t.Fatal(err)
	}
	menuview.NormalizeLayout(&again)
	ra, _ := json.Marshal(l)
	rb, _ := json.Marshal(again)
	if string(ra) != string(rb) {
		t.Errorf("round trip:\n%s\n%s", ra, rb)
	}
}

func TestLayoutUnknownNames(t *testing.T) {
	cases := []struct {
		name string
		edit func(l *bankdoc.Layout)
		path string
	}{
		{"item", func(l *bankdoc.Layout) { l.Tabs[0].Buttons[0].Action.Item = "Calzone" }, "views[0].layout.tabs[0].buttons[0].action.item"},
		{"size", func(l *bankdoc.Layout) { l.Tabs[0].Buttons[0].Action.Size = "20 Party" }, "views[0].layout.tabs[0].buttons[0].action.size"},
		{"category", func(l *bankdoc.Layout) { l.Tabs[0].Buttons[1].Action.Category = "Subs" }, "views[0].layout.tabs[0].buttons[1].action.category"},
		{"deal", func(l *bankdoc.Layout) { l.Tabs[0].Buttons[2].Action.Deal = "Free Soda" }, "views[0].layout.tabs[0].buttons[2].action.deal"},
		{"group", func(l *bankdoc.Layout) { l.Screens[0].Buttons[0].Action.Group = "Sauces" }, "views[0].layout.screens[0].buttons[0].action.group"},
		{"option", func(l *bankdoc.Layout) { l.Screens[0].Buttons[0].Action.Option = "Bacon" }, "views[0].layout.screens[0].buttons[0].action.option"},
		{"flow item", func(l *bankdoc.Layout) { l.Flows["Calzone"] = nil }, "views[0].layout.flows.Calzone"},
		{"flow size", func(l *bankdoc.Layout) { l.Flows["Cheese Pizza"][0].Size = "Huge" }, "views[0].layout.flows.Cheese Pizza[0].size"},
		{"flow twice", func(l *bankdoc.Layout) { l.Flows["cheese pizza"] = nil }, "views[0].layout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := docLayout()
			c.edit(&l)
			_, err := layoutApplier().layoutIDs("views[0].layout", l, map[string]string{"2 medium cheese": "d1"})
			var e *BadDocError
			if !errors.As(err, &e) || !errors.Is(err, ErrBadDocument) {
				t.Fatalf("want a bad document, got %v", err)
			}
			if !strings.HasPrefix(e.Path, c.path) {
				t.Errorf("path %q, want %q", e.Path, c.path)
			}
		})
	}
}
