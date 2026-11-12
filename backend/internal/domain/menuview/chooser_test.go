package menuview

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// chooserLayout adds a size chooser screen ("Pizza Size": a 12" and a 16"
// button) to testLayout and starts Cheese Pizza's flow with it; the tab sells
// Cheese Pizza without a size (MENU-PLAN M2).
func chooserLayout() Layout {
	l := testLayout()
	l.Screens = append(l.Screens, ScreenOf[Action]{Name: "Pizza Size", Prompt: "Which size?", Grid: Grid{Rows: 1, Cols: 2},
		Pages: []Page{{Name: "Size"}}, Buttons: []ButtonOf[Action]{
			{Row: 0, Col: 0, Action: Action{Type: ActSize, SizeID: uid(id(101))}},
			{Row: 0, Col: 1, Caption: "XL", Action: Action{Type: ActSize, SizeID: uid(id(102))}},
		}})
	l.Flows[id(20).String()] = []Step{
		{Screen: "Pizza Toppings", SizeID: uid(id(101))}, // a size-only step is skipped when finding the first step
		{Screen: "pizza size", RequiredCount: 1, Forced: true},
		{Screen: "Pizza Toppings"},
	}
	l.Tabs[0].Buttons = append(l.Tabs[0].Buttons, ButtonOf[Action]{Page: 0, Row: 2, Col: 0, Action: Action{Type: ActItem, ItemID: uid(id(20))}})
	return l
}

func TestSizeChooserValid(t *testing.T) {
	l := chooserLayout()
	NormalizeLayout(&l)
	if ps := ValidateLayout(&l, testRefs()); len(ps) != 0 {
		t.Fatalf("problems: %+v", ps)
	}
	if got := SizeChoosers(&l); !got[id(20).String()] || len(got) != 1 {
		t.Errorf("choosers: %v", got)
	}
}

func TestSizeChooserProblems(t *testing.T) {
	sizeless := "tabs[0].buttons[6].action.size_id"
	cases := []struct {
		name  string
		edit  func(l *Layout)
		path  string
		inMsg string
	}{
		{"size on a tab", func(l *Layout) { l.Tabs[0].Buttons[5].Action = Action{Type: ActSize, SizeID: uid(id(101))} }, "tabs[0].buttons[5].action.type", "must be one of"},
		{"size_id required", func(l *Layout) { l.Screens[1].Buttons[0].Action.SizeID = nil }, "screens[1].buttons[0].action.size_id", "size_id is required"},
		{"unknown size", func(l *Layout) { l.Screens[1].Buttons[0].Action.SizeID = uid(id(77)) }, "screens[1].buttons[0].action.size_id", "not one of this store's sizes"},
		{"item on a size button", func(l *Layout) { l.Screens[1].Buttons[0].Action.ItemID = uid(id(20)) }, "screens[1].buttons[0].action.item_id", "doesn't belong"},
		{"option on a size button", func(l *Layout) {
			l.Screens[1].Buttons[0].Action.GroupID, l.Screens[1].Buttons[0].Action.OptionID = uid(id(201)), uid(id(211))
		}, "screens[1].buttons[0].action.group_id", "doesn't belong"},
		{"mode on a size button", func(l *Layout) { l.Screens[1].Buttons[0].Action.Mode = "no" }, "screens[1].buttons[0].action.mode", "doesn't belong"},
		{"category on a size button", func(l *Layout) { l.Screens[1].Buttons[0].Action.CategoryID = uid(id(10)) }, "screens[1].buttons[0].action.category_id", "doesn't belong"},
		{"first step is not a chooser", func(l *Layout) {
			f := l.Flows[id(20).String()]
			f[1], f[2] = f[2], f[1]
		}, sizeless, "size_id is required"},
		{"no flow", func(l *Layout) { delete(l.Flows, id(20).String()) }, sizeless, "size_id is required"},
		{"chooser screen without size buttons", func(l *Layout) {
			l.Screens[1].Buttons = []ButtonOf[Action]{{Action: Action{Type: ActState, State: "whole"}}}
		}, sizeless, "size_id is required"},
		{"only a size-only step", func(l *Layout) {
			l.Flows[id(20).String()] = []Step{{Screen: "Pizza Size", SizeID: uid(id(101))}}
		}, sizeless, "size_id is required"},
		{"item_pick keeps needing a size", func(l *Layout) {
			l.Screens[0].Buttons[3].Action = Action{Type: ActItemPick, ItemID: uid(id(20))}
		}, "screens[0].buttons[3].action.size_id", "size_id is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := chooserLayout()
			c.edit(&l)
			NormalizeLayout(&l)
			ps := ValidateLayout(&l, testRefs())
			for _, p := range ps {
				if p.Path == c.path && strings.Contains(p.Message, c.inMsg) {
					return
				}
			}
			t.Fatalf("want %s ~ %q, got %+v", c.path, c.inMsg, ps)
		})
	}
}

func TestResolveSizeChooser(t *testing.T) {
	bank := testBank()
	v := View{ID: id(90), Kind: KindCounter, Name: "Counter", Rules: Rules{HiddenCategories: []uuid.UUID{}}}
	l := chooserLayout()
	l.Screens[1].Buttons = append(l.Screens[1].Buttons, ButtonOf[Action]{Row: 0, Col: 1, Page: 0, Action: Action{Type: ActSize, SizeID: uid(id(103))}})
	l.Screens[1].Buttons[2].Col = 0
	l.Screens[1].Pages = append(l.Screens[1].Pages, Page{Name: "More"})
	l.Screens[1].Buttons[2].Page = 1
	NormalizeLayout(&l)
	b := Compile(bank, v, nil, nil)
	ResolveLayout(&b, bank, v, nil, &l)
	scr := b.Layout.Screens[1]
	s12 := scr.Buttons[0]
	if s12.Caption != "12 Medium" || s12.Action.Label != "12 Medium" || s12.Action.SizeName != "12 Medium" || s12.Action.PriceCents != nil || s12.Action.Missing {
		t.Errorf("12 size button: %+v %+v", s12, s12.Action)
	}
	if xl := scr.Buttons[1]; xl.Caption != "XL" || xl.Action.SizeName != "16 X-Large" {
		t.Errorf("16 size button keeps its caption: %+v", xl)
	}
	if gone := scr.Buttons[2].Action; !gone.Missing {
		t.Errorf("a deleted size is missing: %+v", gone)
	}
	found := false
	for _, n := range b.NeedsAttention {
		found = found || (n.Code == "layout_size_missing" && n.ItemID == id(103).String())
	}
	if !found {
		t.Errorf("needs attention: %+v", b.NeedsAttention)
	}
	// The sizeless tab button: the item's name, no size, no price.
	btn := b.Layout.Tabs[0].Buttons[6]
	if btn.Caption != "Cheese Pizza" || btn.Action.SizeName != "" || btn.Action.PriceCents != nil || btn.Action.Missing {
		t.Errorf("sizeless item button: %+v %+v", btn, btn.Action)
	}
}
