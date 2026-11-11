package menuview

import (
	"testing"

	"quiccpos/main/internal/domain/menu"

	"github.com/google/uuid"
)

func TestCounterOnlyMenus(t *testing.T) {
	web, counter, drinks := uuid.New(), uuid.New(), uuid.New()
	cWeb, cCounter, cCounter2, cDrinks := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	m := func(id uuid.UUID, cats ...uuid.UUID) menu.BankMenu {
		bm := menu.BankMenu{Menu: menu.Menu{ID: id}}
		for _, c := range cats {
			bm.Categories = append(bm.Categories, menu.BankCategory{Category: menu.Category{ID: c}})
		}
		return bm
	}
	bank := &menu.Bank{Menus: []menu.BankMenu{m(web, cWeb), m(counter, cCounter, cCounter2), m(drinks, cDrinks)}}

	// No counter view names a menu: nothing is counter-only.
	if got := CounterOnlyMenus(bank, []View{{Kind: KindCounter}, {Kind: KindWeb}}); len(got) != 0 {
		t.Errorf("no menus rule: %v", got)
	}
	// The counter shows Counter + Drinks; the website hides Counter's categories
	// (the converter's way) and shows Drinks → only Counter is counter-only.
	views := []View{
		{Kind: KindCounter, Rules: Rules{Menus: []uuid.UUID{counter, drinks}}},
		{Kind: KindWeb, Rules: Rules{HiddenCategories: []uuid.UUID{cCounter, cCounter2}}},
		{Kind: KindVendor}, // maps only: doesn't count
	}
	got := CounterOnlyMenus(bank, views)
	if !got[counter] || got[drinks] || got[web] || len(got) != 1 {
		t.Errorf("hidden on web: %v", got)
	}
	// The website hiding only one of Counter's categories still shows it.
	views[1].Rules.HiddenCategories = []uuid.UUID{cCounter}
	if got := CounterOnlyMenus(bank, views); got[counter] {
		t.Errorf("partly hidden: %v", got)
	}
	// An operator view limited to the web menu doesn't show Counter either.
	views[1] = View{Kind: KindOperator, Rules: Rules{Menus: []uuid.UUID{web, drinks}}}
	if got := CounterOnlyMenus(bank, views); !got[counter] || got[drinks] {
		t.Errorf("operator menus rule: %v", got)
	}
}

// A view's Menus rule shows only those menus.
func TestCompileMenusRule(t *testing.T) {
	web, counter := uuid.New(), uuid.New()
	cWeb, cCounter := uuid.New(), uuid.New()
	iWeb, iCounter := uuid.New(), uuid.New()
	item := func(id uuid.UUID, name string) menu.BankItem {
		return menu.BankItem{Item: menu.Item{ID: id, Name: name, PriceCents: 100, InStock: true}}
	}
	bank := &menu.Bank{Groups: map[uuid.UUID]menu.BankGroup{}, Menus: []menu.BankMenu{
		{Menu: menu.Menu{ID: web, Available: true}, Categories: []menu.BankCategory{{Category: menu.Category{ID: cWeb, Name: "Pizza", Available: true}, Items: []menu.BankItem{item(iWeb, "Web Pizza")}}}},
		{Menu: menu.Menu{ID: counter, Available: true}, Categories: []menu.BankCategory{{Category: menu.Category{ID: cCounter, Name: "Counter Pizza", Available: true}, Items: []menu.BankItem{item(iCounter, "Counter Cheese")}}}},
	}}
	b := Compile(bank, View{Kind: KindCounter, Rules: Rules{Menus: []uuid.UUID{counter}}}, nil, nil)
	if len(b.Categories) != 1 || b.Categories[0].Items[0].ID != iCounter.String() {
		t.Errorf("counter build: %+v", b.Categories)
	}
	b = Compile(bank, View{Kind: KindWeb}, nil, nil)
	if len(b.Categories) != 2 {
		t.Errorf("no rule shows every menu: %d categories", len(b.Categories))
	}
}
