package menucheck

import (
	"strings"
	"testing"

	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/menuview"

	"github.com/google/uuid"
)

// A small store shaped like Lombardi's, with the real cases of
// .dev/notes/mod-prices-vs-softtouch-2026-11-13.md planted in it.
type fixture struct {
	bank       *menu.Bank
	sizes      map[string]uuid.UUID
	groups     map[string]uuid.UUID
	items      map[string]*menu.BankItem
	options    map[string]uuid.UUID // "group/option"
	menuMain   uuid.UUID
	menuCount  uuid.UUID
	catering   uuid.UUID
	printer    uuid.UUID
	layout     *menuview.Layout
	counterVw  menuview.View
	webVw      menuview.View
	hiddenCats []uuid.UUID
}

func newFixture() *fixture {
	f := &fixture{sizes: map[string]uuid.UUID{}, groups: map[string]uuid.UUID{}, items: map[string]*menu.BankItem{}, options: map[string]uuid.UUID{}}
	f.printer = uuid.New()
	f.bank = &menu.Bank{Version: 7, Groups: map[uuid.UUID]menu.BankGroup{}}
	for i, n := range []string{`10" Small`, `12" Medium`, `14" Large`, `16" X-Large`, "Small", "Large", `8"`, `12"`} {
		id := uuid.New()
		f.sizes[n] = id
		f.bank.Sizes = append(f.bank.Sizes, menu.Size{ID: id, Name: n, Position: i})
	}
	return f
}

func (f *fixture) group(name string, required bool, opts ...menu.BankOption) uuid.UUID {
	id := uuid.New()
	g := menu.BankGroup{ModifierGroup: menu.ModifierGroup{ID: id, Name: name, Required: required, MaxSelect: 0}}
	for _, o := range opts {
		o.ID = uuid.New()
		o.GroupID = id
		g.Options = append(g.Options, o)
		f.options[name+"/"+o.Name] = o.ID
	}
	f.bank.Groups[id] = g
	f.groups[name] = id
	return id
}

func opt(name string, cents int64, sized ...any) menu.BankOption {
	o := menu.BankOption{ModifierOption: menu.ModifierOption{Name: name, PriceDeltaCents: cents, InStock: true}}
	_ = sized
	return o
}

func (f *fixture) sized(o menu.BankOption, prices map[string]int64) menu.BankOption {
	for _, s := range f.bank.Sizes {
		if c, ok := prices[s.Name]; ok {
			o.Prices = append(o.Prices, menu.OptionPrice{SizeID: s.ID, PriceCents: c})
		}
	}
	return o
}

func item(name string, cents int64, groups ...uuid.UUID) menu.BankItem {
	return menu.BankItem{Item: menu.Item{ID: uuid.New(), Name: name, PriceCents: cents, InStock: true}, GroupIDs: groups}
}

func (f *fixture) withSizes(it menu.BankItem, prices map[string]int64) menu.BankItem {
	for _, s := range f.bank.Sizes {
		if c, ok := prices[s.Name]; ok {
			it.Sizes = append(it.Sizes, menu.ItemSize{SizeID: s.ID, PriceCents: c})
		}
	}
	return it
}

func (f *fixture) build() Input {
	f.menuMain, f.menuCount = uuid.New(), uuid.New()
	toppings := f.group("Pizza Toppings", false,
		f.sized(opt("Pepperoni", 125), map[string]int64{`10" Small`: 125, `12" Medium`: 175, `14" Large`: 225, `16" X-Large`: 250}),
		f.sized(opt("Extra Sauce", 0), map[string]int64{`10" Small`: 0, `12" Medium`: 50, `14" Large`: 75, `16" X-Large`: 100}),
		opt("Fresh Mozzarella", 150), // one price: SoftTouch had it on the 10" screen only
		f.sized(opt("Red Onions", 125), map[string]int64{`10" Small`: 125, `12" Medium`: 175, `16" X-Large`: 250}), // no 14"
		f.sized(opt("Well Done", 0), map[string]int64{`10" Small`: 0, `12" Medium`: 0}),
		opt("No Cheese", 0),
	)
	salad := f.group("Salad Mods", false,
		opt("Extra Egg", 75), opt("Add Egg", 0), opt("Add Lettuce", 0), opt("Grilled Chicken", 349), opt("No Onions", 0))
	smallSalad := f.group("Small Salad Mods", false, opt("Sm Grilled Chicken", 199), opt("Add Lettuce", 0))
	pasta := f.group("Pasta Toppings", false, opt("Meatballs", 299), opt("Chicken", 299), opt("Shrimp", 399), opt("Tomato Sauce", 0))
	drinks := f.group("Club Side choice", true, opt("Chips", 0), opt("Fries", 0))
	plate := f.group("Desi plate", true, opt("Chicken Tikka", 1299), opt("Lamb", 1499))
	f.group("Old Sauces", false) // empty, unused
	dressings := f.group("Dressings", false)
	dup := f.group("Wing Sauce", false, opt("Hot", 0), opt("BBQ", 0), opt(" bbq ", 0))
	big := f.group("Two sides", true, opt("Rice", 0))
	g := f.bank.Groups[big]
	g.MinSelect = 2
	f.bank.Groups[big] = g

	pizza := f.withSizes(item("Cheese Pizza", 0, toppings), map[string]int64{`10" Small`: 1099, `12" Medium`: 1299, `14" Large`: 1599, `16" X-Large`: 1799})
	garden := f.withSizes(item("Garden Salad", 0, smallSalad), map[string]int64{"Small": 699, "Large": 999})
	caesar := f.withSizes(item("Caesar Salad", 0, smallSalad, salad), map[string]int64{"Small": 699, "Large": 999})
	boneless := f.withSizes(item("Boneless Wings", 0), map[string]int64{"Small": 0, "Large": 1099})
	ziti := item("Baked Ziti", 1099, pasta)
	club := item("Club Sandwich", 899, drinks)
	desi := item("Desi Special", 0, plate) // priced by its required choice: fine
	napkins := item("Napkins", 0)
	gravy := item("Gravy on Side", 150)
	hot := item("Hot Sauce on Side", 75)
	ice := item("Cup of Ice", 0)
	ice.NoKitchenTicket = true
	wings := item("Wings 10 pc", 1199, dressings, dup)
	stray := item("Stray Item", 500)
	two := item("Two-side plate", 999, big)
	tray := item("Lasagna Tray", 0)
	quick := item("Quick add", 0)
	quick.QuickAdd = true

	kitchen := f.printer
	pizzaCat := menu.BankCategory{Category: menu.Category{ID: uuid.New(), Name: "Pizza", Available: true, DefaultPrinterID: &kitchen},
		Items: []menu.BankItem{pizza, garden, caesar, boneless, ziti, club, desi, wings, two}}
	misc := menu.BankCategory{Category: menu.Category{ID: uuid.New(), Name: "Misc", Available: true},
		Items: []menu.BankItem{napkins, gravy, hot, ice, stray}} // no printer: gravy, hot, napkins, stray print everywhere
	misc.Items[1].PrinterID = &kitchen
	misc.Items[2].PrinterID = &kitchen
	misc.Items[0].PrinterID = &kitchen
	catering := menu.BankCategory{Category: menu.Category{ID: uuid.New(), Name: "Catering", Available: true, DefaultPrinterID: &kitchen},
		Items: []menu.BankItem{tray}}
	f.catering = catering.ID
	quickCat := menu.BankCategory{Category: menu.Category{ID: uuid.New(), Name: "Quick add", Available: true}, Items: []menu.BankItem{quick}}
	f.bank.Menus = []menu.BankMenu{
		{Menu: menu.Menu{ID: f.menuMain, Name: "Lombardi's", Available: true}, Categories: []menu.BankCategory{pizzaCat, misc}},
		{Menu: menu.Menu{ID: f.menuCount, Name: "Counter only", Available: true}, Categories: []menu.BankCategory{catering, quickCat}},
	}
	for mi := range f.bank.Menus {
		for ci := range f.bank.Menus[mi].Categories {
			for ii := range f.bank.Menus[mi].Categories[ci].Items {
				it := &f.bank.Menus[mi].Categories[ci].Items[ii]
				f.items[it.Name] = it
			}
		}
	}

	// The counter: every item has a tab button but "Stray Item"; the
	// Garden Salad's Large step opens the Salad Mods screen (its group isn't
	// on the item: the large-salad bug).
	saladGroup := f.groups["Salad Mods"]
	egg := f.options["Salad Mods/Extra Egg"]
	largeID := f.sizes["Large"]
	f.layout = &menuview.Layout{
		Tabs:    []menuview.TabOf[menuview.Action]{{Name: "Food", Grid: menuview.Grid{Rows: 6, Cols: 6}}},
		Screens: []menuview.ScreenOf[menuview.Action]{{Name: "Salad Mods", Buttons: []menuview.ButtonOf[menuview.Action]{{Action: menuview.Action{Type: menuview.ActOption, GroupID: &saladGroup, OptionID: &egg}}}}},
		Flows: map[string][]menuview.Step{
			garden.ID.String(): {{Screen: "Salad Mods", SizeID: &largeID}},
			caesar.ID.String(): {{Screen: "Salad Mods", SizeID: &largeID}},
		},
	}
	for _, it := range []menu.BankItem{pizza, garden, caesar, boneless, ziti, club, desi, wings, two, napkins, gravy, hot, ice} {
		id := it.ID
		f.layout.Tabs[0].Buttons = append(f.layout.Tabs[0].Buttons, menuview.ButtonOf[menuview.Action]{Action: menuview.Action{Type: menuview.ActItem, ItemID: &id}})
	}
	f.counterVw = menuview.View{ID: uuid.New(), Kind: menuview.KindCounter, Name: "Counter", HasLayout: true,
		Rules: menuview.Rules{Menus: []uuid.UUID{f.menuMain, f.menuCount}, HiddenCategories: []uuid.UUID{f.catering}}}
	f.webVw = menuview.View{ID: uuid.New(), Kind: menuview.KindWeb, Name: "Website", Rules: menuview.Rules{Menus: []uuid.UUID{f.menuMain}}}
	return Input{
		Bank: f.bank,
		Views: []View{
			{View: f.webVw, Build: menuview.Compile(f.bank, f.webVw, nil, nil)},
			{View: f.counterVw, Build: menuview.Compile(f.bank, f.counterVw, nil, nil)},
		},
		Layout: f.layout,
	}
}

func find(r Report, code, name string) *Problem {
	for i := range r.Problems {
		p := &r.Problems[i]
		if p.Code == code && (p.ItemName == name || p.OptionName == name || (p.ItemName == "" && p.GroupName == name)) {
			return p
		}
	}
	return nil
}

func must(t *testing.T, r Report, code, name, want string) *Problem {
	t.Helper()
	p := find(r, code, name)
	if p == nil {
		t.Fatalf("%s %q: not found; got %+v", code, name, codes(r))
	}
	if want != "" && !strings.Contains(p.Message, want) {
		t.Fatalf("%s %q: message %q lacks %q", code, name, p.Message, want)
	}
	return p
}

func mustNot(t *testing.T, r Report, code, name string) {
	t.Helper()
	if p := find(r, code, name); p != nil {
		t.Fatalf("%s %q: unexpected %+v", code, name, *p)
	}
}

func codes(r Report) []string {
	var out []string
	for _, p := range r.Problems {
		out = append(out, p.Code+":"+p.ItemName+p.GroupName+"/"+p.OptionName)
	}
	return out
}

func TestPOS(t *testing.T) {
	f := newFixture()
	r := Run(f.build())
	p := must(t, r, CodeNoCounterButton, "Stray Item", "no button")
	if p.Severity != SevError || len(p.Channels) != 2 {
		t.Fatalf("stray: %+v", *p)
	}
	tray := must(t, r, CodeHiddenOnCounter, "Lasagna Tray", "“Catering” is hidden on the counter")
	if tray.Severity != SevInfo || !strings.Contains(tray.Message, "website or the call center either") {
		t.Fatalf("tray: %+v", *tray)
	}
	mustNot(t, r, CodeNoCounterButton, "Cheese Pizza")
	mustNot(t, r, CodeNoCounterButton, "Quick add")
	mustNot(t, r, CodeHiddenOnCounter, "Quick add")
	if r.Counts[CheckPOS] != 1 { // the hidden tray is info, never counted
		t.Fatalf("pos count %d", r.Counts[CheckPOS])
	}
	if r.Version != 7 {
		t.Fatalf("version %d", r.Version)
	}
}

func TestPOSCategoryButtonReaches(t *testing.T) {
	f := newFixture()
	in := f.build()
	cat := f.bank.Menus[0].Categories[1].ID // Misc
	in.Layout.Tabs[0].Buttons = append(in.Layout.Tabs[0].Buttons, menuview.ButtonOf[menuview.Action]{Action: menuview.Action{Type: menuview.ActCategory, CategoryID: &cat}})
	mustNot(t, Run(in), CodeNoCounterButton, "Stray Item")
}

func TestPOSWithoutLayout(t *testing.T) {
	f := newFixture()
	in := f.build()
	in.Layout = nil
	r := Run(in)
	mustNot(t, r, CodeNoCounterButton, "Stray Item")
	must(t, r, CodeHiddenOnCounter, "Lasagna Tray", "")
}

func TestPrinters(t *testing.T) {
	f := newFixture()
	r := Run(f.build())
	must(t, r, CodeNoPrinter, "Stray Item", "every station")
	must(t, r, CodeNoPrinter, "Quick add", "typed items")
	mustNot(t, r, CodeNoPrinter, "Cup of Ice")   // receipt only
	mustNot(t, r, CodeNoPrinter, "Cheese Pizza") // its category's printer
	mustNot(t, r, CodeNoPrinter, "Napkins")      // its own printer
	mustNot(t, r, CodeNoPrinter, "Lasagna Tray") // its category's printer
	if r.Counts[CheckPrinter] != 2 {
		t.Fatalf("printer count %d: %v", r.Counts[CheckPrinter], codes(r))
	}
}

func TestPrinterCategoryNoKitchen(t *testing.T) {
	f := newFixture()
	in := f.build()
	f.bank.Menus[0].Categories[1].NoKitchenTicket = true // Misc: receipt only for items without a printer
	mustNot(t, Run(in), CodeNoPrinter, "Stray Item")
}

func TestItemPrices(t *testing.T) {
	f := newFixture()
	r := Run(f.build())
	p := must(t, r, CodeSizeZero, "Boneless Wings", "Small is $0.00 while Large has a price")
	if len(p.Sizes) != 1 || p.Sizes[0] != "Small" {
		t.Fatalf("sizes %v", p.Sizes)
	}
	must(t, r, CodeItemZero, "Napkins", "of the 4 other items in Misc cost money")
	mustNot(t, r, CodeItemZero, "Desi Special") // priced by its required choice
	mustNot(t, r, CodeItemZero, "Cup of Ice")   // receipt only
	mustNot(t, r, CodeItemZero, "Lasagna Tray") // sold nowhere
	mustNot(t, r, CodeItemZero, "Quick add")
	mustNot(t, r, CodeItemZero, "Cheese Pizza") // its sizes are priced
}

func TestOptionPrices(t *testing.T) {
	f := newFixture()
	r := Run(f.build())
	p := must(t, r, CodeOptionSizeZero, "Extra Sauce", `$0.00 at 10" Small, but $0.50 at 12" Medium`)
	if p.GroupName != "Pizza Toppings" || len(p.Sizes) != 1 {
		t.Fatalf("extra sauce: %+v", *p)
	}
	must(t, r, CodeOptionSomeSizes, "Red Onions", `No price for 14" Large`)
	must(t, r, CodeOptionFlat, "Fresh Mozzarella", "$1.50 at every size")
	must(t, r, CodeOptionZero, "Add Egg", "while “Extra Egg” is $0.75")
	must(t, r, CodeOptionZero, "Tomato Sauce", "3 of the other 3 choices")
	mustNot(t, r, CodeOptionSizeZero, "Well Done") // free on purpose
	mustNot(t, r, CodeOptionZero, "Add Lettuce")   // a free fixing
	mustNot(t, r, CodeOptionZero, "No Onions")
	mustNot(t, r, CodeOptionZero, "Chips") // a free choice
	mustNot(t, r, CodeOptionZero, "No Cheese")
}

func TestMods(t *testing.T) {
	f := newFixture()
	r := Run(f.build())
	p := must(t, r, CodeScreenWrongGroup, "Garden Salad", "“Salad Mods” at Large picks from “Salad Mods”")
	if p.Severity != SevError || p.GroupName != "Salad Mods" || len(p.Sizes) != 1 || p.Sizes[0] != "Large" {
		t.Fatalf("garden: %+v", *p)
	}
	mustNot(t, r, CodeScreenWrongGroup, "Caesar Salad") // it has the group
	e := must(t, r, CodeEmptyGroup, "Dressings", "1 item uses it")
	if e.Severity != SevError {
		t.Fatalf("dressings: %+v", *e)
	}
	o := must(t, r, CodeEmptyGroup, "Old Sauces", "no item uses it")
	if o.Severity != SevReview {
		t.Fatalf("old sauces: %+v", *o)
	}
	must(t, r, CodeDuplicateOption, "Wing Sauce", "“BBQ” is in it 2 times")
	must(t, r, CodeGroupTooSmall, "Two sides", "Asks for 2 picks but has only 1 option")
	if r.Counts[CheckMods] != 5 { // review counts too; only info is left out
		t.Fatalf("mods count %d: %v", r.Counts[CheckMods], codes(r))
	}
}

func TestNoBank(t *testing.T) {
	r := Run(Input{})
	if len(r.Problems) != 0 || r.Problems == nil {
		t.Fatalf("%+v", r)
	}
}
