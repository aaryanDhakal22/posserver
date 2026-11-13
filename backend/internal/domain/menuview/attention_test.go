package menuview

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestBrokenButtonsSayWhereTheyAre(t *testing.T) {
	bank := testBank()
	v := View{ID: id(90), Kind: KindCounter, Name: "Counter"}
	l := testLayout()
	NormalizeLayout(&l)
	// The owner deletes Cheese Pizza (two buttons, one with a caption, and a
	// flow) and Wings 8pc (one button, captioned "Wings").
	bank.Menus[0].Categories[0].Items = nil
	b := Compile(bank, v, nil, nil)
	ResolveLayout(&b, bank, v, nil, &l)

	var lines []NeedsAttention
	for _, n := range b.NeedsAttention {
		if n.Code == "layout_item_missing" {
			lines = append(lines, n)
		}
	}
	if len(lines) != 3 {
		t.Fatalf("one line per broken button (3), got %d: %+v", len(lines), lines)
	}
	byCell := map[[3]int]NeedsAttention{}
	for _, n := range lines {
		if n.WhereKind != "tab" || n.Where != "Pizza" || n.Cell == nil || n.Name != "" {
			t.Fatalf("line without its place: %+v", n)
		}
		byCell[[3]int{n.Cell.Page, n.Cell.Row, n.Cell.Col}] = n
	}
	wings := byCell[[3]int{0, 0, 1}]
	if wings.Caption != "Wings" || wings.ItemID != id(21).String() || wings.Cell.PageName != "12\" Med" {
		t.Errorf("wings button: %+v %+v", wings, wings.Cell)
	}
	if d := wings.Describe(); d != "Button “Wings” on tab “Pizza” (page “12\" Med”, row 1, column 2): its item was deleted (item "+id(21).String()[:8]+")" {
		t.Errorf("describe: %s", d)
	}
	med := byCell[[3]int{0, 0, 0}]
	if med.Caption != "12\" Med Cheese" || med.ItemID != id(20).String() {
		t.Errorf("captioned pizza button: %+v", med)
	}
	// No caption and no bank name: the short id names it.
	xl := byCell[[3]int{1, 0, 0}]
	if xl.Caption != "" || xl.Cell.PageName != "16\" XL" || !strings.HasPrefix(xl.Describe(), "Button on tab “Pizza” (page “16\" XL”, row 1, column 1): its item was deleted (item ") {
		t.Errorf("uncaptioned button: %+v → %s", xl, xl.Describe())
	}
}

func TestBrokenFlowWithNoButton(t *testing.T) {
	bank := testBank()
	v := View{ID: id(90), Kind: KindCounter, Name: "Counter"}
	l := testLayout()
	// Cheese Pizza's buttons are gone already; its flow stays behind.
	l.Tabs[0].Buttons = l.Tabs[0].Buttons[2:]
	NormalizeLayout(&l)
	bank.Menus[0].Categories[0].Items = bank.Menus[0].Categories[0].Items[1:]
	b := Compile(bank, v, nil, nil)
	ResolveLayout(&b, bank, v, nil, &l)
	var flow *NeedsAttention
	for i, n := range b.NeedsAttention {
		if n.Code == "layout_item_missing" {
			if flow != nil {
				t.Fatalf("one line expected: %+v", b.NeedsAttention)
			}
			flow = &b.NeedsAttention[i]
		}
	}
	if flow == nil || flow.WhereKind != "flow" || flow.Cell != nil || flow.ItemID != id(20).String() {
		t.Fatalf("flow line: %+v", flow)
	}
	if !strings.HasPrefix(flow.Describe(), "The counter flow of a deleted item (item ") {
		t.Errorf("describe: %s", flow.Describe())
	}
}

func TestGoneEntrySaysWhatItChanged(t *testing.T) {
	bank := testBank()
	gone := uuid.MustParse("1a2b3c4d-0000-4000-8000-000000000001")
	price := int64(1299)
	entries := []Entry{
		{ID: id(70), Kind: EntryItem, Anchor: Anchor{ItemIDs: []uuid.UUID{gone}}, Hidden: true, PriceOverrideCents: &price, Category: "Salads"},
		{ID: id(71), Kind: EntryItem, Anchor: Anchor{ItemIDs: []uuid.UUID{gone}}, DisplayName: " Garden Salad "},
		{ID: id(72), Kind: EntryMerge, Anchor: Anchor{ItemIDs: []uuid.UUID{id(30), gone}}, DisplayName: "Cheesecake"},
	}
	b := Compile(bank, View{ID: id(91), Kind: KindWeb, Name: "Website"}, entries, nil)
	got := map[string]NeedsAttention{}
	for _, n := range b.NeedsAttention {
		if n.Code == "anchor_gone" {
			got[n.EntryID] = n
		}
	}
	if len(got) != 3 {
		t.Fatalf("anchor_gone lines: %+v", b.NeedsAttention)
	}
	a := got[id(70).String()]
	if a.ItemID != gone.String() || a.EntryKind != EntryItem || a.Change != "hidden, $12.99, moved to “Salads”" || a.PriceCents == nil || *a.PriceCents != 1299 {
		t.Errorf("unnamed entry: %+v", a)
	}
	if d := a.Describe(); d != "A change (hidden, $12.99, moved to “Salads”): its item was deleted (item 1a2b3c4d)" {
		t.Errorf("describe: %s", d)
	}
	if d := got[id(71).String()].Describe(); d != "Change “Garden Salad”: its item was deleted (item 1a2b3c4d)" {
		t.Errorf("describe named: %s", d)
	}
	if d := got[id(72).String()].Describe(); d != "Combined item “Cheesecake”: one of its items was deleted (item 1a2b3c4d)" {
		t.Errorf("describe merge: %s", d)
	}
}

func TestRemoveBrokenButtons(t *testing.T) {
	bank := testBank()
	l := testLayout()
	NormalizeLayout(&l)
	refs := LayoutRefs{Bank: bank, Deals: map[string]string{"deal-1": "2 Medium Cheese"}}
	bank.Menus[0].Categories[0].Items = nil // Cheese Pizza + Wings 8pc deleted
	before := ValidateLayout(&l, refs)
	if len(before) != 4 {
		t.Fatalf("three broken buttons + a broken flow expected, got %+v", before)
	}
	// The Wings button alone: the rest stay broken, nothing new.
	nb, nf := RemoveFromLayout(&l, bank, []ButtonRef{{WhereKind: "tab", Where: " pizza ", Page: 0, Row: 0, Col: 1}}, nil)
	if nb != 1 || nf != 0 || len(l.Tabs[0].Buttons) != 5 {
		t.Fatalf("removed %d buttons %d flows: %+v", nb, nf, l.Tabs[0].Buttons)
	}
	after := ValidateLayout(&l, refs)
	if !NoNewProblems(before, after) || len(after) != len(before)-1 {
		t.Errorf("after one removal: %+v", after)
	}
	// Cheese Pizza's last buttons: its flow goes with them.
	nb, nf = RemoveFromLayout(&l, bank, []ButtonRef{{WhereKind: "tab", Where: "Pizza", Page: 0}, {WhereKind: "tab", Where: "Pizza", Page: 1},
		{WhereKind: "screen", Where: "Pizza", Page: 0}}, nil)
	if nb != 2 || nf != 1 || len(l.Flows) != 0 {
		t.Fatalf("removed %d buttons %d flows, flows left %v", nb, nf, l.Flows)
	}
	if ps := ValidateLayout(&l, refs); len(ps) != 0 {
		t.Errorf("a clean layout now: %+v", ps)
	}
	// A problem that wasn't there before is new.
	if NoNewProblems(nil, []Problem{{Path: "tabs[0].buttons[1].action.item_id", Message: "x"}}) {
		t.Error("a new problem passed")
	}
	if !NoNewProblems([]Problem{{Path: "tabs[0].buttons[4].x", Message: "m"}}, []Problem{{Path: "tabs[0].buttons[3].x", Message: "m"}}) {
		t.Error("a renumbered button's problem counted as new")
	}
}
