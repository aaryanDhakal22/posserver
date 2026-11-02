package menuview

import (
	"strings"
	"testing"

	"quiccpos/main/internal/domain/menu"

	"github.com/google/uuid"
)

func id(n byte) uuid.UUID { return uuid.UUID{n} }

func ptr(v int64) *int64 { return &v }

// testBank: Pizza (sizes 12/16, toppings group), Wings, Cheesecake ×2 flavors,
// a hidden-able Drinks category with Soda (sold out).
func testBank() *menu.Bank {
	s12, s16 := id(101), id(102)
	toppings := menu.BankGroup{ModifierGroup: menu.ModifierGroup{ID: id(201), Name: "Toppings", MaxSelect: 5, AllowMultiple: true},
		Portions: true, Options: []menu.BankOption{
			{ModifierOption: menu.ModifierOption{ID: id(211), Name: "Pepperoni", PriceDeltaCents: 150, InStock: true},
				Prices: []menu.OptionPrice{{SizeID: s12, PriceCents: 150}, {SizeID: s16, PriceCents: 225}}},
			{ModifierOption: menu.ModifierOption{ID: id(212), Name: "Cheese", InStock: true}, IsDefault: true},
		}}
	return &menu.Bank{
		LocationID: id(1), Version: 7,
		Sizes:  []menu.Size{{ID: s12, Name: "12 Medium"}, {ID: s16, Name: "16 X-Large"}},
		Groups: map[uuid.UUID]menu.BankGroup{toppings.ID: toppings},
		Menus: []menu.BankMenu{{Menu: menu.Menu{ID: id(2), Name: "Main", Available: true}, Categories: []menu.BankCategory{
			{Category: menu.Category{ID: id(10), Name: "Pizza", Available: true}, Items: []menu.BankItem{
				{Item: menu.Item{ID: id(20), Name: "Cheese Pizza", PriceCents: 0, InStock: true, Position: 1},
					Sizes: []menu.ItemSize{{SizeID: s12, PriceCents: 1199}, {SizeID: s16, PriceCents: 1599}}, GroupIDs: []uuid.UUID{id(201)}},
				{Item: menu.Item{ID: id(21), Name: "Wings 8pc", PriceCents: 1099, InStock: true, Position: 2}},
			}},
			{Category: menu.Category{ID: id(11), Name: "Desserts", Available: true}, Items: []menu.BankItem{
				{Item: menu.Item{ID: id(30), Name: "Cheesecake Strawberry", PriceCents: 599, InStock: true}},
				{Item: menu.Item{ID: id(31), Name: "Cheesecake Oreo", PriceCents: 599, InStock: false}},
			}},
			{Category: menu.Category{ID: id(12), Name: "Drinks", Available: true}, Items: []menu.BankItem{
				{Item: menu.Item{ID: id(40), Name: "Soda", PriceCents: 199, InStock: false}},
			}},
		}}},
	}
}

func TestApplyRule(t *testing.T) {
	cases := []struct {
		cents int64
		bps   int
		round string
		want  int64
	}{
		{1199, 0, RoundNone, 1199},
		{1199, 2000, RoundNone, 1439}, // 14.388 → up to the cent
		{1199, 2000, RoundCeil99, 1499},
		{1499, 0, RoundCeil99, 1499},
		{1400, 0, RoundCeil99, 1499},
		{1250, 2000, RoundCeil99, 1599},
		{0, 2000, RoundCeil99, 0},
		{150, 2000, RoundCeil99, 199},
	}
	for _, c := range cases {
		if got := ApplyRule(c.cents, c.bps, c.round); got != c.want {
			t.Errorf("ApplyRule(%d, %d, %s) = %d, want %d", c.cents, c.bps, c.round, got, c.want)
		}
	}
}

func TestCompileBankAsIs(t *testing.T) {
	b := Compile(testBank(), View{ID: id(90), Kind: KindWeb, Name: "Web"}, nil, nil)
	if b.Version != 7 || len(b.Sizes) != 2 || len(b.Categories) != 3 {
		t.Fatalf("build: v%d sizes %d cats %d", b.Version, len(b.Sizes), len(b.Categories))
	}
	pizza := b.Categories[0].Items[0]
	if pizza.ID != id(20).String() || len(pizza.Sizes) != 2 || pizza.Sizes[1].PriceCents != 1599 || pizza.Sizes[1].Name != "16 X-Large" {
		t.Errorf("pizza: %+v", pizza)
	}
	g := pizza.ModifierGroups[0]
	if !g.Portions || g.Options[0].Prices[1].PriceCents != 225 || !g.Options[1].IsDefault {
		t.Errorf("toppings: %+v", g)
	}
	if !b.Categories[2].Items[0].SoldOut {
		t.Error("sold-out soda must say so")
	}
	if len(b.NeedsAttention) != 0 {
		t.Errorf("attention: %+v", b.NeedsAttention)
	}
}

func TestCompileRuleAndEntries(t *testing.T) {
	v := View{ID: id(90), Kind: KindWeb, Rules: Rules{MarkupBps: 2000, Round: RoundCeil99, HiddenCategories: []uuid.UUID{id(12)}, RequirePhoto: true}}
	entries := []Entry{
		{ID: id(50), Kind: EntryItem, Anchor: Anchor{ItemIDs: []uuid.UUID{id(21)}}, DisplayName: "Party Wings (8)", Photo: "/w.jpg",
			Category: "Wings", PriceOverrideCents: ptr(1299)},
		{ID: id(51), Kind: EntryItem, Anchor: Anchor{ItemIDs: []uuid.UUID{id(20)}}, Photo: "/p.jpg", Extra: Extra{SizePrices: map[string]int64{id(102).String(): 1999}}},
		{ID: id(52), Kind: EntryMerge, Anchor: Anchor{ItemIDs: []uuid.UUID{id(30), id(31)}}, DisplayName: "Cheesecake", Photo: "/c.jpg",
			Extra: Extra{Labels: map[string]string{id(30).String(): "Strawberry"}}},
		{ID: id(53), Kind: EntryAugment, Anchor: Anchor{ItemIDs: []uuid.UUID{id(20), id(21)}}, DisplayName: "Pizza + Wings", Photo: "/d.jpg",
			Category: "Deals", PriceOverrideCents: ptr(2299)},
		{ID: id(54), Kind: EntryItem, Anchor: Anchor{ItemIDs: []uuid.UUID{id(99)}}, DisplayName: "Gone"},
		{ID: id(55), Kind: EntryAugment, DisplayName: "No price", Anchor: Anchor{ItemIDs: []uuid.UUID{id(21)}}},
	}
	b := Compile(testBank(), v, entries, nil)
	byName := map[string]BuildItem{}
	cats := map[string]bool{}
	for _, c := range b.Categories {
		cats[c.Name] = true
		for _, it := range c.Items {
			byName[it.Name] = it
		}
	}
	if cats["Drinks"] {
		t.Error("hidden category shown")
	}
	w := byName["Party Wings (8)"]
	if w.PriceCents != 1299 || w.Anchor.ItemIDs[0] != id(21) || w.ID != id(21).String() {
		t.Errorf("override: %+v", w)
	}
	p := byName["Cheese Pizza"]
	if p.Sizes[0].PriceCents != 1499 || p.Sizes[1].PriceCents != 1999 {
		t.Errorf("rule + size override: %+v", p.Sizes)
	}
	if p.ModifierGroups[0].Options[0].Prices[0].PriceCents != 199 || p.ModifierGroups[0].Options[1].PriceCents != 0 {
		t.Errorf("option prices follow the rule ($0 stays $0): %+v", p.ModifierGroups[0].Options)
	}
	c := byName["Cheesecake"]
	if c.ID != id(52).String() || len(c.Choices) != 2 || c.Choices[0].Name != "Strawberry" || c.Choices[1].Name != "Cheesecake Oreo" ||
		!c.Choices[1].SoldOut || c.SoldOut {
		t.Errorf("merge: %+v", c)
	}
	if _, ok := byName["Cheesecake Strawberry"]; ok {
		t.Error("merged bank items must not show on their own")
	}
	d := byName["Pizza + Wings"]
	if d.PriceCents != 2299 || len(d.Anchor.ItemIDs) != 2 || !cats["Deals"] || !cats["Wings"] {
		t.Errorf("augment: %+v cats %v", d, cats)
	}
	codes := map[string]int{}
	for _, n := range b.NeedsAttention {
		codes[n.Code]++
	}
	if codes["anchor_gone"] != 1 || codes["bad_augment"] != 1 || codes["no_photo"] != 0 {
		t.Errorf("attention: %+v", b.NeedsAttention)
	}
}

// A merge of a category's items keeps that category's place, and leaves with
// it when the category is hidden.
func TestCompileMergeKeepsCategoryPlace(t *testing.T) {
	merge := []Entry{{ID: id(80), Kind: EntryMerge, Anchor: Anchor{ItemIDs: []uuid.UUID{id(30), id(31)}}, DisplayName: "Cheesecake"}}
	b := Compile(testBank(), View{Kind: KindWeb}, merge, nil)
	var names []string
	for _, c := range b.Categories {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "Pizza,Desserts,Drinks" {
		t.Errorf("categories: %v", names)
	}
	b = Compile(testBank(), View{Kind: KindWeb, Rules: Rules{HiddenCategories: []uuid.UUID{id(11)}}}, merge, nil)
	for _, c := range b.Categories {
		if c.Name == "Desserts" {
			t.Error("a merge in a hidden category shown")
		}
	}
}

func TestCompileHideAndMove(t *testing.T) {
	entries := []Entry{
		{ID: id(60), Kind: EntryItem, Anchor: Anchor{ItemIDs: []uuid.UUID{id(21)}}, Hidden: true},
		{ID: id(61), Kind: EntryMerge, Anchor: Anchor{ItemIDs: []uuid.UUID{id(30)}}, DisplayName: "One"},
	}
	b := Compile(testBank(), View{Kind: KindOperator}, entries, nil)
	for _, c := range b.Categories {
		for _, it := range c.Items {
			if it.Name == "Wings 8pc" {
				t.Error("hidden item shown")
			}
		}
	}
	if len(b.NeedsAttention) != 1 || b.NeedsAttention[0].Code != "bad_merge" {
		t.Errorf("attention: %+v", b.NeedsAttention)
	}
}

func TestCompileVendor(t *testing.T) {
	entries := []Entry{
		{ID: id(70), Kind: EntryMap, Anchor: Anchor{ItemIDs: []uuid.UUID{id(21)}}, Extra: Extra{VendorName: "Jumbo  Party Wings"}},
		{ID: id(71), Kind: EntryMap, Anchor: Anchor{ItemIDs: []uuid.UUID{id(99)}}, Extra: Extra{VendorName: "Ghost"}},
		{ID: id(72), Kind: EntryMap, Extra: Extra{VendorName: "x"}},
	}
	unmatched := []UnmatchedName{{Name: "jumbo party wings", Orders: 29}, {Name: "Veggie Wrap", Orders: 1}}
	b := Compile(testBank(), View{Kind: KindVendor}, entries, unmatched)
	if len(b.Categories) != 0 || len(b.Maps) != 1 || b.Maps[0].ItemID != id(21) {
		t.Fatalf("vendor build: %+v", b)
	}
	codes := map[string]string{}
	for _, n := range b.NeedsAttention {
		codes[n.Code] += n.Name + ";"
	}
	if codes["unmatched"] != "Veggie Wrap;" || codes["anchor_gone"] != "Ghost;" || codes["bad_map"] != ";" {
		t.Errorf("attention: %+v", b.NeedsAttention)
	}
}

func TestNormName(t *testing.T) {
	if NormName("  Regular Tomato  Cheese ") != "regular tomato cheese" {
		t.Error(NormName("  Regular Tomato  Cheese "))
	}
}
