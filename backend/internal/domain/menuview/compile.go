package menuview

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"quiccpos/main/internal/domain/menu"

	"github.com/google/uuid"
)

// Build is a view compiled against the bank at one menu version: what a
// channel of the view's kind shows (web / operator / counter) or how a
// vendor's names map to the bank (vendor).
// BuildFormat is the shape of a Build; a cached build of another format is
// compiled again (2: modifier groups listed once, items refer to them).
const BuildFormat = 2

type Build struct {
	Format     int             `json:"build_format"`
	View       BuildView       `json:"view"`
	Version    int64           `json:"version"`
	Currency   string          `json:"currency"`
	Sizes      []BuildSize     `json:"sizes"`
	Categories []BuildCategory `json:"categories"`
	// ModifierGroups are the groups the shown items use, each once, in the
	// order first used (items refer to them by id).
	ModifierGroups []BuildGroup     `json:"modifier_groups"`
	Maps           []BuildMap       `json:"maps,omitempty"`
	NeedsAttention []NeedsAttention `json:"needs_attention"`
	// Layout is a counter view's layout (ResolveLayout), absent without one.
	Layout *BuildLayout `json:"layout,omitempty"`
}

type BuildView struct {
	ID   uuid.UUID `json:"id"`
	Kind Kind      `json:"kind"`
	Name string    `json:"name"`
}

type BuildSize struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Short string    `json:"short"`
}

type BuildCategory struct {
	Key      string      `json:"key"`
	Name     string      `json:"name"`
	Position int         `json:"position"`
	Items    []BuildItem `json:"items"`
}

// BuildItem is one thing a channel can sell. ID is the bank item's id for an
// item the view shows as is (or changed by an item entry), the entry's id for
// a merge or an augment; Anchor says which bank items it is.
type BuildItem struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Photo       string          `json:"photo"`
	Anchor      Anchor          `json:"anchor"`
	PriceCents  int64           `json:"price_cents"`
	Sizes       []BuildItemSize `json:"sizes"`
	SoldOut     bool            `json:"sold_out"`
	// ModifierGroupIDs are the item's groups, in order (Build.ModifierGroups
	// holds each group once).
	ModifierGroupIDs []uuid.UUID   `json:"modifier_group_ids"`
	Choices          []BuildChoice `json:"choices,omitempty"`
	position         int
}

type BuildItemSize struct {
	SizeID     uuid.UUID `json:"size_id"`
	Name       string    `json:"name"`
	PriceCents int64     `json:"price_cents"`
}

// BuildChoice is one bank item a merged view item stands for.
type BuildChoice struct {
	ItemID     uuid.UUID       `json:"item_id"`
	Name       string          `json:"name"`
	PriceCents int64           `json:"price_cents"`
	Sizes      []BuildItemSize `json:"sizes"`
	SoldOut    bool            `json:"sold_out"`
}

type BuildGroup struct {
	ID            uuid.UUID     `json:"id"`
	Name          string        `json:"name"`
	Required      bool          `json:"required"`
	MinSelect     int           `json:"min_select"`
	MaxSelect     int           `json:"max_select"`
	AllowMultiple bool          `json:"allow_multiple"`
	Portions      bool          `json:"portions"`
	Options       []BuildOption `json:"options"`
}

type BuildOption struct {
	ID         uuid.UUID          `json:"id"`
	Name       string             `json:"name"`
	PriceCents int64              `json:"price_cents"`
	Prices     []menu.OptionPrice `json:"prices"`
	IsDefault  bool               `json:"is_default"`
	SoldOut    bool               `json:"sold_out"`
	// ShowOnDriverTicket: print the choice on the delivery (driver) ticket
	// even at $0 (00046; the Pi reads it from the counter build).
	ShowOnDriverTicket bool `json:"show_on_driver_ticket,omitempty"`
}

// BuildMap is one vendor name → bank item mapping (vendor views).
type BuildMap struct {
	EntryID    uuid.UUID  `json:"entry_id"`
	VendorName string     `json:"vendor_name"`
	VendorSize string     `json:"vendor_size"`
	ItemID     uuid.UUID  `json:"item_id"`
	SizeID     *uuid.UUID `json:"size_id,omitempty"`
}

// NeedsAttention is something the view can't place or a rule flags.
type NeedsAttention struct {
	Code    string `json:"code"`
	EntryID string `json:"entry_id,omitempty"`
	ItemID  string `json:"item_id,omitempty"`
	Name    string `json:"name,omitempty"`
	Orders  int64  `json:"orders,omitempty"`
	Message string `json:"message"`
}

// UnmatchedName is a vendor item name orders carried that no menu item or
// map matched (vendor views list them for a decision).
type UnmatchedName struct {
	Name   string
	Orders int64
}

// Compile builds a view of the bank. unmatched (vendor views only) are the
// vendor names recent orders carried that nothing matched.
func Compile(bank *menu.Bank, v View, entries []Entry, unmatched []UnmatchedName) Build {
	b := Build{
		Format:         BuildFormat,
		View:           BuildView{ID: v.ID, Kind: v.Kind, Name: v.Name},
		Version:        bank.Version,
		Currency:       "USD",
		Sizes:          []BuildSize{},
		Categories:     []BuildCategory{},
		ModifierGroups: []BuildGroup{},
		NeedsAttention: []NeedsAttention{},
	}
	for _, s := range bank.Sizes {
		b.Sizes = append(b.Sizes, BuildSize{ID: s.ID, Name: s.Name, Short: s.Short})
	}
	if v.Kind == KindVendor {
		compileVendor(&b, bank, entries, unmatched)
		return b
	}
	c := compiler{bank: bank, rules: v.Rules, build: &b, cats: map[string]*BuildCategory{}, catPos: map[uuid.UUID]int{},
		groupSeen: map[uuid.UUID]bool{}, sellable: map[uuid.UUID]bool{}, viewHidden: map[uuid.UUID]bool{}, bankCats: map[string]bankCat{}}
	c.run(entries)
	return b
}

type compiler struct {
	bank  *menu.Bank
	rules Rules
	build *Build
	cats  map[string]*BuildCategory
	order []string
	// catPos is each shown bank category's place; a merge / augment that
	// lands in one keeps it (absent = hidden or in a hidden menu).
	catPos    map[uuid.UUID]int
	groupSeen map[uuid.UUID]bool
	// sellable: the item's menu and category are on. viewHidden: the view
	// hides it (its category by rule, or an item entry).
	sellable, viewHidden map[uuid.UUID]bool
	// bankCats: the shown bank categories by lowercased name, so an entry
	// "moved" into one of them joins it instead of making a twin.
	bankCats map[string]bankCat
}

type bankCat struct {
	key, name string
	pos       int
}

func (c *compiler) category(key, name string, pos int) *BuildCategory {
	if cat, ok := c.cats[key]; ok {
		return cat
	}
	c.cats[key] = &BuildCategory{Key: key, Name: name, Position: pos, Items: []BuildItem{}}
	c.order = append(c.order, key)
	return c.cats[key]
}

// named is the category an entry moves its item into: the shown bank
// category of that name, else a view category of its own at pos.
func (c *compiler) named(name string, pos int) *BuildCategory {
	name = strings.TrimSpace(name)
	if b, ok := c.bankCats[strings.ToLower(name)]; ok {
		return c.category(b.key, b.name, b.pos)
	}
	return c.category("c:"+name, name, pos)
}

func (c *compiler) attention(n NeedsAttention) {
	c.build.NeedsAttention = append(c.build.NeedsAttention, n)
}

func (c *compiler) run(entries []Entry) {
	hiddenCat := map[uuid.UUID]bool{}
	for _, id := range c.rules.HiddenCategories {
		hiddenCat[id] = true
	}
	// A view limited to some menus hides every other menu's categories.
	if len(c.rules.Menus) > 0 {
		only := map[uuid.UUID]bool{}
		for _, id := range c.rules.Menus {
			only[id] = true
		}
		for _, m := range c.bank.Menus {
			if !only[m.ID] {
				for _, cat := range m.Categories {
					hiddenCat[cat.ID] = true
				}
			}
		}
	}
	byItem := map[uuid.UUID]*Entry{} // item entries by their one anchor
	merged := map[uuid.UUID]bool{}   // bank items shown inside a merge
	for i := range entries {
		e := &entries[i]
		switch e.Kind {
		case EntryItem:
			if len(e.Anchor.ItemIDs) != 1 {
				c.attention(NeedsAttention{Code: "bad_anchor", EntryID: e.ID.String(), Message: "an item entry anchors exactly one bank item"})
				continue
			}
			if _, _, ok := c.bank.Item(e.Anchor.ItemIDs[0]); !ok {
				c.attention(NeedsAttention{Code: "anchor_gone", EntryID: e.ID.String(), ItemID: e.Anchor.ItemIDs[0].String(),
					Name: e.DisplayName, Message: "the bank item this entry changes is gone"})
				continue
			}
			byItem[e.Anchor.ItemIDs[0]] = e
		case EntryMerge:
			for _, id := range e.Anchor.ItemIDs {
				merged[id] = true
			}
		}
	}

	// What can be sold, what the view hides, and the shown categories.
	pos := 0
	for _, m := range c.bank.Menus {
		for _, cat := range m.Categories {
			pos++
			on := m.Available && cat.Available
			for _, it := range cat.Items {
				c.sellable[it.ID] = on
				if e := byItem[it.ID]; hiddenCat[cat.ID] || e != nil && e.Hidden {
					c.viewHidden[it.ID] = true
				}
			}
			if k := strings.ToLower(strings.TrimSpace(cat.Name)); on && !hiddenCat[cat.ID] && k != "" {
				if _, dup := c.bankCats[k]; !dup {
					c.bankCats[k] = bankCat{key: cat.ID.String(), name: cat.Name, pos: pos}
				}
			}
		}
	}

	// Bank items, menu by menu, category by category.
	pos = 0
	for _, m := range c.bank.Menus {
		if !m.Available {
			continue
		}
		for _, cat := range m.Categories {
			pos++
			if !cat.Available || hiddenCat[cat.ID] {
				continue
			}
			c.catPos[cat.ID] = pos
			for _, it := range cat.Items {
				if merged[it.ID] || it.QuickAdd { // Quick add (00008) is sold from no menu
					continue
				}
				e := byItem[it.ID]
				if e != nil && e.Hidden {
					continue
				}
				bi := c.item(it, e)
				target := c.category(cat.ID.String(), cat.Name, pos)
				if e != nil && strings.TrimSpace(e.Category) != "" {
					target = c.named(e.Category, pos)
				}
				target.Items = append(target.Items, bi)
			}
		}
	}

	// Merges and augments.
	for i := range entries {
		e := &entries[i]
		switch e.Kind {
		case EntryMerge:
			c.merge(e)
		case EntryAugment:
			c.augment(e)
		}
	}

	for _, key := range c.order {
		cat := c.cats[key]
		if len(cat.Items) == 0 {
			continue
		}
		sort.SliceStable(cat.Items, func(i, j int) bool { return cat.Items[i].position < cat.Items[j].position })
		c.build.Categories = append(c.build.Categories, *cat)
	}
	sort.SliceStable(c.build.Categories, func(i, j int) bool { return c.build.Categories[i].Position < c.build.Categories[j].Position })
}

// item is a bank item as the view shows it (e = its item entry, may be nil).
func (c *compiler) item(it menu.BankItem, e *Entry) BuildItem {
	bi := BuildItem{
		ID: it.ID.String(), Name: it.Name, Description: it.Description, Anchor: Anchor{ItemIDs: []uuid.UUID{it.ID}},
		PriceCents: c.price(it.PriceCents), Sizes: c.sizes(it.Sizes, nil), SoldOut: !it.InStock,
		ModifierGroupIDs: c.groups(it.GroupIDs), position: it.Position,
	}
	if e != nil {
		if strings.TrimSpace(e.DisplayName) != "" {
			bi.Name = strings.TrimSpace(e.DisplayName)
		}
		if e.Description != "" {
			bi.Description = e.Description
		}
		bi.Photo = e.Photo
		bi.position = e.Position
		if e.PriceOverrideCents != nil {
			bi.PriceCents = *e.PriceOverrideCents
		}
		if len(e.Extra.SizePrices) > 0 {
			bi.Sizes = c.sizes(it.Sizes, e.Extra.SizePrices)
		}
	}
	if c.rules.RequirePhoto && bi.Photo == "" {
		c.attention(NeedsAttention{Code: "no_photo", ItemID: it.ID.String(), Name: bi.Name, Message: "no photo yet"})
	}
	return bi
}

func (c *compiler) merge(e *Entry) {
	if len(e.Anchor.ItemIDs) < 2 {
		c.attention(NeedsAttention{Code: "bad_merge", EntryID: e.ID.String(), Name: e.DisplayName, Message: "a merge needs two or more bank items"})
		return
	}
	var first *menu.BankItem
	var firstCat *menu.BankCategory
	bi := BuildItem{ID: e.ID.String(), Name: strings.TrimSpace(e.DisplayName), Description: e.Description, Photo: e.Photo,
		Anchor: Anchor{ItemIDs: e.Anchor.ItemIDs}, Sizes: []BuildItemSize{}, ModifierGroupIDs: []uuid.UUID{}, position: e.Position}
	allOut := true
	for _, id := range e.Anchor.ItemIDs {
		it, cat, ok := c.bank.Item(id)
		if !ok {
			c.attention(NeedsAttention{Code: "anchor_gone", EntryID: e.ID.String(), ItemID: id.String(), Name: e.DisplayName,
				Message: "a bank item in this merge is gone"})
			continue
		}
		if !c.sellable[id] || c.viewHidden[id] {
			continue // its menu / category is off, or the view hides it
		}
		if first == nil {
			first, firstCat = it, cat
		}
		name := it.Name
		if l := e.Extra.Labels[id.String()]; strings.TrimSpace(l) != "" {
			name = strings.TrimSpace(l)
		}
		bi.Choices = append(bi.Choices, BuildChoice{ItemID: it.ID, Name: name, PriceCents: c.price(it.PriceCents),
			Sizes: c.sizes(it.Sizes, nil), SoldOut: !it.InStock})
		if it.InStock {
			allOut = false
		}
	}
	if first == nil {
		return
	}
	if bi.Name == "" {
		bi.Name = first.Name
	}
	bi.PriceCents = c.price(first.PriceCents)
	if e.PriceOverrideCents != nil {
		bi.PriceCents = *e.PriceOverrideCents
	}
	bi.Sizes = c.sizes(first.Sizes, nil)
	bi.ModifierGroupIDs = c.groups(first.GroupIDs)
	bi.SoldOut = allOut
	c.place(bi, e, firstCat)
}

func (c *compiler) augment(e *Entry) {
	if strings.TrimSpace(e.DisplayName) == "" || e.PriceOverrideCents == nil || len(e.Anchor.ItemIDs) == 0 {
		c.attention(NeedsAttention{Code: "bad_augment", EntryID: e.ID.String(), Name: e.DisplayName,
			Message: "an added item needs a name, a price and at least one bank item"})
		return
	}
	var firstCat *menu.BankCategory
	soldOut := false
	for _, id := range e.Anchor.ItemIDs {
		it, cat, ok := c.bank.Item(id)
		if !ok {
			c.attention(NeedsAttention{Code: "anchor_gone", EntryID: e.ID.String(), ItemID: id.String(), Name: e.DisplayName,
				Message: "a bank item in this added item is gone"})
			return
		}
		if !c.sellable[id] {
			return // a deal with an item whose menu / category is off can't be sold
		}
		if firstCat == nil {
			firstCat = cat
		}
		if !it.InStock {
			soldOut = true
		}
	}
	bi := BuildItem{ID: e.ID.String(), Name: strings.TrimSpace(e.DisplayName), Description: e.Description, Photo: e.Photo,
		Anchor: Anchor{ItemIDs: e.Anchor.ItemIDs}, PriceCents: *e.PriceOverrideCents, Sizes: []BuildItemSize{},
		SoldOut: soldOut, ModifierGroupIDs: []uuid.UUID{}, position: e.Position}
	c.place(bi, e, firstCat)
}

// place puts a merge / augment into its entry's category (after the bank's
// categories), else the first anchored item's bank category, in its place —
// and leaves it out when that category is hidden.
func (c *compiler) place(bi BuildItem, e *Entry, bankCat *menu.BankCategory) {
	if e.Hidden {
		return
	}
	var target *BuildCategory
	if name := strings.TrimSpace(e.Category); name != "" {
		target = c.named(name, 1<<30)
	} else if bankCat == nil {
		return
	} else if pos, shown := c.catPos[bankCat.ID]; shown {
		target = c.category(bankCat.ID.String(), bankCat.Name, pos)
	} else {
		return
	}
	target.Items = append(target.Items, bi)
}

func (c *compiler) sizes(in []menu.ItemSize, override map[string]int64) []BuildItemSize {
	out := []BuildItemSize{}
	for _, s := range in {
		size, ok := c.bank.SizeByID(s.SizeID)
		if !ok {
			continue
		}
		p := c.price(s.PriceCents)
		if v, ok := override[s.SizeID.String()]; ok {
			p = v
		}
		out = append(out, BuildItemSize{SizeID: s.SizeID, Name: size.Name, PriceCents: p})
	}
	return out
}

// groups returns the item's group ids (those the bank still has) and adds
// each group to the build the first time an item uses it.
func (c *compiler) groups(ids []uuid.UUID) []uuid.UUID {
	out := []uuid.UUID{}
	for _, id := range ids {
		g, ok := c.bank.Groups[id]
		if !ok {
			continue
		}
		out = append(out, id)
		if c.groupSeen[id] {
			continue
		}
		c.groupSeen[id] = true
		bg := BuildGroup{ID: g.ID, Name: g.Name, Required: g.Required, MinSelect: g.MinSelect, MaxSelect: g.MaxSelect,
			AllowMultiple: g.AllowMultiple, Portions: g.Portions, Options: []BuildOption{}}
		for _, o := range g.Options {
			prices := []menu.OptionPrice{}
			for _, p := range o.Prices {
				prices = append(prices, menu.OptionPrice{SizeID: p.SizeID, PriceCents: c.optionPrice(p.PriceCents)})
			}
			bg.Options = append(bg.Options, BuildOption{ID: o.ID, Name: o.Name, PriceCents: c.optionPrice(o.PriceDeltaCents),
				Prices: prices, IsDefault: o.IsDefault, SoldOut: !o.InStock, ShowOnDriverTicket: o.ShowOnDriverTicket})
		}
		c.build.ModifierGroups = append(c.build.ModifierGroups, bg)
	}
	return out
}

// price applies the view's price rule to a bank price.
func (c *compiler) price(cents int64) int64 {
	return ApplyRule(cents, c.rules.MarkupBps, c.rules.Round)
}

// optionPrice is a modifier option's price: the markup, never the .99
// rounding (a $1.50 topping must not become $1.99).
func (c *compiler) optionPrice(cents int64) int64 {
	return ApplyRule(cents, c.rules.MarkupBps, RoundNone)
}

// ApplyRule raises a bank price by markupBps (rounding up to a whole cent),
// then rounds it (RoundCeil99: up to the next X.99). $0 stays $0.
func ApplyRule(cents int64, markupBps int, round string) int64 {
	if cents <= 0 {
		return cents
	}
	p := cents
	if markupBps != 0 {
		p = (cents*int64(10000+markupBps) + 9999) / 10000
	}
	if round == RoundCeil99 && (markupBps != 0 || p%100 != 99) {
		p = (p/100)*100 + 99
	}
	return p
}

// --- vendor views -------------------------------------------------------------

func compileVendor(b *Build, bank *menu.Bank, entries []Entry, unmatched []UnmatchedName) {
	b.Maps = []BuildMap{}
	mapped := map[string]bool{}
	for _, e := range entries {
		if e.Kind != EntryMap {
			continue
		}
		name := strings.TrimSpace(e.Extra.VendorName)
		if name == "" || len(e.Anchor.ItemIDs) != 1 {
			b.NeedsAttention = append(b.NeedsAttention, NeedsAttention{Code: "bad_map", EntryID: e.ID.String(),
				Message: "a mapping needs the vendor's item name and one bank item"})
			continue
		}
		if _, _, ok := bank.Item(e.Anchor.ItemIDs[0]); !ok {
			b.NeedsAttention = append(b.NeedsAttention, NeedsAttention{Code: "anchor_gone", EntryID: e.ID.String(), Name: name,
				ItemID: e.Anchor.ItemIDs[0].String(), Message: fmt.Sprintf("“%s” maps to a bank item that is gone", name)})
			continue
		}
		b.Maps = append(b.Maps, BuildMap{EntryID: e.ID, VendorName: name, VendorSize: strings.TrimSpace(e.Extra.VendorSize),
			ItemID: e.Anchor.ItemIDs[0], SizeID: e.Anchor.SizeID})
		mapped[NormName(name)] = true
	}
	sort.SliceStable(b.Maps, func(i, j int) bool { return NormName(b.Maps[i].VendorName) < NormName(b.Maps[j].VendorName) })
	for _, u := range unmatched {
		if mapped[NormName(u.Name)] {
			continue
		}
		b.NeedsAttention = append(b.NeedsAttention, NeedsAttention{Code: "unmatched", Name: u.Name, Orders: u.Orders,
			Message: fmt.Sprintf("“%s” matches no menu item: it prints on every station", u.Name)})
	}
}

// NormName is how vendor item names compare: lower case, runs of whitespace
// collapsed (the same rule as the kitchen routing's name match).
func NormName(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), unicode.IsSpace), " ")
}
