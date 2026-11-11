package menuview

import (
	"encoding/json"
	"strings"
	"testing"

	"quiccpos/main/internal/domain/menu"

	"github.com/google/uuid"
)

func uid(u uuid.UUID) *uuid.UUID { return &u }

// testLayout is a small valid layout on testBank: a Pizza tab (a sized
// button, a category, a deal, a function) and a toppings screen with state
// buttons, flows for the pizza (one step per size + an any-size step).
func testLayout() Layout {
	return Layout{
		TabStrip: Grid{Rows: 2, Cols: 4},
		Tabs: []TabOf[Action]{{
			Name: "Pizza", StripRow: 0, StripCol: 0, Color: "#FF8080", FontColor: "#000000", FontSize: 14,
			Grid: Grid{Rows: 6, Cols: 3}, Pages: []Page{{Name: "12\" Med"}, {Name: "16\" XL"}},
			Buttons: []ButtonOf[Action]{
				{Page: 0, Row: 0, Col: 0, Caption: "12\" Med Cheese", Action: Action{Type: ActItem, ItemID: uid(id(20)), SizeID: uid(id(101))}},
				{Page: 1, Row: 0, Col: 0, Action: Action{Type: ActItem, ItemID: uid(id(20)), SizeID: uid(id(102))}},
				{Page: 0, Row: 0, Col: 1, Caption: "Wings", Action: Action{Type: ActItem, ItemID: uid(id(21))}},
				{Page: 0, Row: 1, Col: 0, Action: Action{Type: ActCategory, CategoryID: uid(id(11))}},
				{Page: 0, Row: 1, Col: 1, Action: Action{Type: ActDeal, DealID: "deal-1"}},
				{Page: 0, Row: 1, Col: 2, Caption: "Open item", Action: Action{Type: ActFunction, Name: "open_item"}},
			},
		}},
		Screens: []ScreenOf[Action]{{
			Name: "Pizza Toppings", Prompt: "Toppings:", Grid: Grid{Rows: 5, Cols: 5},
			Pages: []Page{{Name: "Add Mods"}, {Name: "1/2 Topping"}},
			Buttons: []ButtonOf[Action]{
				{Page: 0, Row: 0, Col: 0, Color: "#ff0000", Action: Action{Type: ActOption, GroupID: uid(id(201)), OptionID: uid(id(211))}},
				{Page: 1, Row: 0, Col: 0, Action: Action{Type: ActState, State: "whole"}},
				{Page: 1, Row: 0, Col: 1, Action: Action{Type: ActState, State: "first_only"}},
				{Page: 1, Row: 1, Col: 0, Action: Action{Type: ActItemPick, CategoryID: uid(id(12))}},
				{Page: 0, Row: 0, Col: 1, Caption: "No Pepperoni", Action: Action{Type: ActOption, GroupID: uid(id(201)), OptionID: uid(id(211)), Mode: "no"}},
			},
		}},
		Flows: map[string][]Step{id(20).String(): {
			{Screen: "Pizza Toppings", SizeID: uid(id(101)), RequiredCount: 1, Forced: true},
			{Screen: "pizza toppings"},
		}},
		Functions: Functions{Nav: []Function{{Name: "fcalc", Caption: "Calculator", Order: 1, Visible: true}},
			Manager: []Function{{Name: "zout", Caption: "Z Out", Order: 17, Visible: true, Role: "manager"}}},
		BottomBar: BottomBar{None: Bar{Rows: 2, Cols: 9, Buttons: []BarButton{{Row: 0, Col: 0, Name: "home", Caption: "Home"},
			{Row: 0, Col: 8, Name: "cash_out", Caption: "Cash Out", Emphasis: true}}}},
	}
}

func testRefs() LayoutRefs {
	return LayoutRefs{Bank: testBank(), Deals: map[string]string{"deal-1": "2 Medium Cheese"}}
}

func TestLayoutValid(t *testing.T) {
	l := testLayout()
	NormalizeLayout(&l)
	if ps := ValidateLayout(&l, testRefs()); len(ps) != 0 {
		t.Fatalf("problems: %+v", ps)
	}
	if l.Tabs[0].Color != "#ff8080" || l.Functions.Employee == nil || l.BottomBar.Line.Buttons == nil {
		t.Errorf("not normalized: %+v", l.Tabs[0])
	}
	// The stored shape round-trips through JSON unchanged.
	raw, _ := json.Marshal(l)
	var back Layout
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	NormalizeLayout(&back)
	raw2, _ := json.Marshal(back)
	if string(raw) != string(raw2) {
		t.Errorf("round trip changed:\n%s\n%s", raw, raw2)
	}
}

func TestLayoutProblems(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(l *Layout)
		path  string
		inMsg string
	}{
		{"unknown item", func(l *Layout) { l.Tabs[0].Buttons[2].Action.ItemID = uid(id(99)) }, "tabs[0].buttons[2].action.item_id", "not in this store"},
		{"size required", func(l *Layout) { l.Tabs[0].Buttons[0].Action.SizeID = nil }, "tabs[0].buttons[0].action.size_id", "size_id is required"},
		{"size not the item's", func(l *Layout) { l.Tabs[0].Buttons[0].Action.SizeID = uid(id(77)) }, "tabs[0].buttons[0].action.size_id", "not one of"},
		{"unsized item takes no size", func(l *Layout) { l.Tabs[0].Buttons[2].Action.SizeID = uid(id(101)) }, "tabs[0].buttons[2].action.size_id", "has no sizes"},
		{"unknown category", func(l *Layout) { l.Tabs[0].Buttons[3].Action.CategoryID = uid(id(98)) }, "tabs[0].buttons[3].action.category_id", "not in this store"},
		{"unknown deal", func(l *Layout) { l.Tabs[0].Buttons[4].Action.DealID = "nope" }, "tabs[0].buttons[4].action.deal_id", "not one of this store's deals"},
		{"bad function name", func(l *Layout) { l.Tabs[0].Buttons[5].Action.Name = "Open Item" }, "tabs[0].buttons[5].action.name", "lower case"},
		{"screen action on a tab", func(l *Layout) { l.Tabs[0].Buttons[5].Action = Action{Type: ActState, State: "no"} }, "tabs[0].buttons[5].action.type", "must be one of"},
		{"tab action on a screen", func(l *Layout) {
			l.Screens[0].Buttons[1].Action = Action{Type: ActItem, ItemID: uid(id(21))}
		}, "screens[0].buttons[1].action.type", "must be one of"},
		{"unknown group", func(l *Layout) { l.Screens[0].Buttons[0].Action.GroupID = uid(id(250)) }, "screens[0].buttons[0].action.group_id", "not in this store"},
		{"option not in group", func(l *Layout) { l.Screens[0].Buttons[0].Action.OptionID = uid(id(250)) }, "screens[0].buttons[0].action.option_id", "not in group"},
		{"bad state", func(l *Layout) { l.Screens[0].Buttons[1].Action.State = "left" }, "screens[0].buttons[1].action.state", "must be one of"},
		{"bad option mode", func(l *Layout) { l.Screens[0].Buttons[0].Action.Mode = "only" }, "screens[0].buttons[0].action.mode", "must be one of"},
		{"mode on a state button", func(l *Layout) { l.Screens[0].Buttons[1].Action.Mode = "no" }, "screens[0].buttons[1].action.mode", "doesn't belong"},
		{"item_pick both", func(l *Layout) { l.Screens[0].Buttons[3].Action.ItemID = uid(id(40)) }, "screens[0].buttons[3].action", "not both"},
		{"stray field", func(l *Layout) { l.Tabs[0].Buttons[3].Action.State = "whole" }, "tabs[0].buttons[3].action.state", "doesn't belong"},
		{"outside grid", func(l *Layout) { l.Tabs[0].Buttons[2].Col = 3 }, "tabs[0].buttons[2]", "outside the 6×3 grid"},
		{"overlap", func(l *Layout) { l.Tabs[0].Buttons[2].Col = 0 }, "tabs[0].buttons[2]", "overlaps buttons[0]"},
		{"same cell other page is fine (control)", func(l *Layout) { l.Tabs[0].Buttons[1].Page = 1 }, "", ""},
		{"bad page", func(l *Layout) { l.Tabs[0].Buttons[1].Page = 2 }, "tabs[0].buttons[1].page", "doesn't exist"},
		{"no pages", func(l *Layout) { l.Screens[0].Pages = nil; l.Screens[0].Buttons = nil }, "screens[0].pages", "1..16 pages"},
		{"bad colour", func(l *Layout) { l.Tabs[0].Buttons[0].Color = "red" }, "tabs[0].buttons[0].color", "#rrggbb"},
		{"short colour", func(l *Layout) { l.Tabs[0].FontColor = "#fff" }, "tabs[0].font_color", "#rrggbb"},
		{"font size", func(l *Layout) { l.Tabs[0].FontSize = 200 }, "tabs[0].font_size", "font size"},
		{"grid too big", func(l *Layout) { l.Tabs[0].Grid.Cols = 13 }, "tabs[0].grid.cols", "1..12"},
		{"strip cell outside", func(l *Layout) { l.Tabs[0].StripCol = 4 }, "tabs[0]", "outside the 2×4 tab strip"},
		{"strip cell taken", func(l *Layout) {
			t2 := l.Tabs[0]
			t2.Name, t2.Buttons = "Subs", nil
			l.Tabs = append(l.Tabs, t2)
		}, "tabs[1]", "taken by tabs[0]"},
		{"duplicate tab", func(l *Layout) {
			t2 := l.Tabs[0]
			t2.StripCol, t2.Buttons, t2.Name = 1, nil, " pizza "
			l.Tabs = append(l.Tabs, t2)
		}, "tabs[1].name", "already at tabs[0]"},
		{"duplicate screen", func(l *Layout) {
			l.Screens = append(l.Screens, ScreenOf[Action]{Name: "PIZZA toppings", Grid: Grid{1, 1}, Pages: []Page{{"P"}}})
		}, "screens[1].name", "already at"},
		{"long caption", func(l *Layout) { l.Tabs[0].Buttons[0].Caption = strings.Repeat("x", 81) }, "tabs[0].buttons[0].caption", "longer than 80"},
		{"control chars", func(l *Layout) { l.Tabs[0].Buttons[0].Caption = "a\x1bp" }, "tabs[0].buttons[0].caption", "control characters"},
		{"flow key not an id", func(l *Layout) { l.Flows["Cheese Pizza"] = nil }, "flows.Cheese Pizza", "item id"},
		{"flow item unknown", func(l *Layout) { l.Flows[id(99).String()] = nil }, "flows." + id(99).String(), "not in this store"},
		{"flow screen unknown", func(l *Layout) { l.Flows[id(20).String()][1].Screen = "Wing Sauce" }, "flows." + id(20).String() + "[1].screen", "no screen named"},
		{"flow size not the item's", func(l *Layout) { l.Flows[id(20).String()][0].SizeID = uid(id(77)) }, "flows." + id(20).String() + "[0].size_id", "not one of"},
		{"required count", func(l *Layout) { l.Flows[id(20).String()][0].RequiredCount = -1 }, "flows." + id(20).String() + "[0].required_count", "0..99"},
		{"role", func(l *Layout) { l.Functions.Nav[0].Role = "owner" }, "functions.nav[0].role", "role must be"},
		{"function twice", func(l *Layout) { l.Functions.Nav = append(l.Functions.Nav, l.Functions.Nav[0]) }, "functions.nav[1].name", "twice"},
		{"bar overlap", func(l *Layout) { l.BottomBar.None.Buttons[1].Col = 0 }, "bottom_bar.none.buttons[1]", "overlaps"},
		{"bar without grid", func(l *Layout) { l.BottomBar.Line.Buttons = []BarButton{{Name: "done"}} }, "bottom_bar.line", "needs rows and cols"},
		{"bar too tall", func(l *Layout) { l.BottomBar.None.Rows = 5 }, "bottom_bar.none.rows", "1..4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := testLayout()
			c.edit(&l)
			NormalizeLayout(&l)
			ps := ValidateLayout(&l, testRefs())
			if c.path == "" {
				if len(ps) != 0 {
					t.Fatalf("want no problem, got %+v", ps)
				}
				return
			}
			for _, p := range ps {
				if p.Path == c.path && strings.Contains(p.Message, c.inMsg) {
					return
				}
			}
			t.Fatalf("want %s ~ %q, got %+v", c.path, c.inMsg, ps)
		})
	}
}

func TestLayoutReportsEveryProblem(t *testing.T) {
	l := testLayout()
	l.Tabs[0].Buttons[0].Color = "red"
	l.Tabs[0].Buttons[2].Action.ItemID = uid(id(99))
	l.Screens[0].Buttons[1].Action.State = "left"
	NormalizeLayout(&l)
	if ps := ValidateLayout(&l, testRefs()); len(ps) != 3 {
		t.Fatalf("want 3 problems, got %+v", ps)
	}
	// A flood stops at MaxProblems.
	for i := 0; i < 300; i++ {
		l.Tabs[0].Buttons = append(l.Tabs[0].Buttons, ButtonOf[Action]{Page: 9, Action: Action{Type: "x"}})
	}
	if ps := ValidateLayout(&l, testRefs()); len(ps) != MaxProblems {
		t.Fatalf("want %d problems, got %d", MaxProblems, len(ps))
	}
}

func TestLayoutLimits(t *testing.T) {
	l := testLayout()
	l.Flows = map[string][]Step{id(20).String(): make([]Step, MaxSteps+1)}
	for i := range l.Flows[id(20).String()] {
		l.Flows[id(20).String()][i].Screen = "Pizza Toppings"
	}
	NormalizeLayout(&l)
	ps := ValidateLayout(&l, testRefs())
	if len(ps) != 1 || !strings.Contains(ps[0].Message, "at most 20 steps") {
		t.Fatalf("got %+v", ps)
	}
}

func TestResolveLayout(t *testing.T) {
	bank := testBank()
	v := View{ID: id(90), Kind: KindCounter, Name: "Counter", Rules: Rules{HiddenCategories: []uuid.UUID{id(12)}}}
	b := Compile(bank, v, nil, nil)
	l := testLayout()
	// A deleted deal and an option of a group no shown item uses.
	l.Tabs[0].Buttons[4].Action.DealID = "gone"
	NormalizeLayout(&l)
	before := len(b.ModifierGroups)
	ResolveLayout(&b, bank, v, map[string]string{"deal-1": "2 Medium Cheese"}, &l)
	if b.Layout == nil {
		t.Fatal("no layout in the build")
	}
	tab := b.Layout.Tabs[0]
	med := tab.Buttons[0]
	if med.Caption != "12\" Med Cheese" || med.Action.Label != "Cheese Pizza" || med.Action.SizeName != "12 Medium" ||
		med.Action.PriceCents == nil || *med.Action.PriceCents != 1199 {
		t.Errorf("12 button: %+v %+v", med, med.Action)
	}
	xl := tab.Buttons[1]
	if xl.Caption != "16 X-Large Cheese Pizza" || *xl.Action.PriceCents != 1599 {
		t.Errorf("16 button (caption from the bank): %+v", xl)
	}
	if c := tab.Buttons[3]; c.Caption != "Desserts" || c.Action.Missing {
		t.Errorf("category: %+v", c)
	}
	if d := tab.Buttons[4]; !d.Action.Missing {
		t.Errorf("gone deal not missing: %+v", d)
	}
	if f := tab.Buttons[5]; f.Caption != "Open item" || f.Action.Label != "open_item" {
		t.Errorf("function: %+v", f)
	}
	scr := b.Layout.Screens[0]
	if o := scr.Buttons[0].Action; o.Label != "Pepperoni" || o.PriceCents == nil || *o.PriceCents != 150 {
		t.Errorf("option: %+v", o)
	}
	if s := scr.Buttons[2]; s.Caption != "FIRST ONLY" {
		t.Errorf("state caption: %q", s.Caption)
	}
	// Drinks is hidden by the view: its item_pick category is missing.
	if p := scr.Buttons[3].Action; !p.Missing || p.Label != "Drinks" {
		t.Errorf("hidden category pick: %+v", p)
	}
	if len(b.ModifierGroups) != before {
		t.Errorf("toppings already in the build, groups %d → %d", before, len(b.ModifierGroups))
	}
	codes := map[string]bool{}
	for _, n := range b.NeedsAttention {
		codes[n.Code] = true
	}
	if !codes["layout_deal_missing"] {
		t.Errorf("needs attention: %+v", b.NeedsAttention)
	}
	if len(b.Layout.Flows[id(20).String()]) != 2 {
		t.Errorf("flows: %+v", b.Layout.Flows)
	}
}

func TestResolveLayoutAddsScreenGroups(t *testing.T) {
	bank := testBank()
	sauce := menu.BankGroup{ModifierGroup: menu.ModifierGroup{ID: id(202), Name: "Wing Sauce"}, Options: []menu.BankOption{
		{ModifierOption: menu.ModifierOption{ID: id(221), Name: "Hot", PriceDeltaCents: 50, InStock: true}}}}
	bank.Groups[sauce.ID] = sauce
	v := View{ID: id(90), Kind: KindCounter, Name: "Counter", Rules: Rules{MarkupBps: 1000}}
	b := Compile(bank, v, nil, nil)
	l := testLayout()
	l.Screens[0].Buttons[0].Action = Action{Type: ActOption, GroupID: uid(id(202)), OptionID: uid(id(221))}
	NormalizeLayout(&l)
	ResolveLayout(&b, bank, v, nil, &l)
	found := false
	for _, g := range b.ModifierGroups {
		found = found || g.ID == id(202)
	}
	if !found {
		t.Errorf("screen group not added: %+v", b.ModifierGroups)
	}
	if p := b.Layout.Screens[0].Buttons[0].Action.PriceCents; p == nil || *p != 55 {
		t.Errorf("option price with the view's markup: %v", p)
	}
	// Removing the item: its button and flow are flagged, not dropped.
	bank.Menus[0].Categories[0].Items = bank.Menus[0].Categories[0].Items[1:]
	b = Compile(bank, v, nil, nil)
	ResolveLayout(&b, bank, v, nil, &l)
	if !b.Layout.Tabs[0].Buttons[0].Action.Missing {
		t.Errorf("deleted item not missing")
	}
	n := 0
	for _, a := range b.NeedsAttention {
		if a.Code == "layout_item_missing" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("one needs-attention line per missing item, got %d: %+v", n, b.NeedsAttention)
	}
}
