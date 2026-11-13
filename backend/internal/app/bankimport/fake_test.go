package bankimport

import (
	"context"
	"encoding/json"
	"sort"

	"quiccpos/main/internal/domain/bankdoc"
	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/menuview"
	"quiccpos/main/internal/domain/pricing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// fakeStore is an in-memory bank for the import's unit tests: sizes, groups
// with options, menus → categories → items, views with entries and layouts,
// deals. Bank() returns a deep copy, so the applier never sees its writes
// until it reads again (like the real transaction). Families are no-ops.
type fakeStore struct {
	sizes   []menu.Size
	groups  map[uuid.UUID]menu.BankGroup
	menus   []menu.Menu
	cats    []menu.Category
	items   []menu.BankItem
	views   []menuview.View
	entries map[uuid.UUID][]menuview.Entry
	layouts map[uuid.UUID]*menuview.Layout
	deals   []pricing.StoreDeal
	seq     byte
	version int64
}

func newFakeStore() *fakeStore {
	return &fakeStore{groups: map[uuid.UUID]menu.BankGroup{}, entries: map[uuid.UUID][]menuview.Entry{},
		layouts: map[uuid.UUID]*menuview.Layout{}, seq: 100}
}

func (f *fakeStore) id() uuid.UUID {
	f.seq++
	f.version++
	return uuid.UUID{0xfe, f.seq}
}

func clone[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}

// InTx runs fn on the store; a dry run's rollback restores a snapshot.
func (f *fakeStore) InTx(_ context.Context, _, _ uuid.UUID, fn func(bankdoc.Tx) error) error {
	snap := clone(fakeSnap{f.sizes, f.groups, f.menus, f.cats, f.items, f.views, f.entries, f.layouts, f.deals})
	err := fn(f)
	if err != nil {
		f.sizes, f.groups, f.menus, f.cats, f.items = snap.Sizes, snap.Groups, snap.Menus, snap.Cats, snap.Items
		f.views, f.entries, f.layouts, f.deals = snap.Views, snap.Entries, snap.Layouts, snap.Deals
		if f.groups == nil {
			f.groups = map[uuid.UUID]menu.BankGroup{}
		}
		if f.entries == nil {
			f.entries = map[uuid.UUID][]menuview.Entry{}
		}
		if f.layouts == nil {
			f.layouts = map[uuid.UUID]*menuview.Layout{}
		}
	}
	if err == bankdoc.ErrRollback {
		return nil
	}
	return err
}

type fakeSnap struct {
	Sizes   []menu.Size
	Groups  map[uuid.UUID]menu.BankGroup
	Menus   []menu.Menu
	Cats    []menu.Category
	Items   []menu.BankItem
	Views   []menuview.View
	Entries map[uuid.UUID][]menuview.Entry
	Layouts map[uuid.UUID]*menuview.Layout
	Deals   []pricing.StoreDeal
}

func (f *fakeStore) Bank(context.Context) (*menu.Bank, error) {
	b := &menu.Bank{Version: f.version, Sizes: clone(f.sizes), Groups: clone(f.groups), Families: []menu.Family{}}
	if b.Sizes == nil {
		b.Sizes = []menu.Size{}
	}
	sort.SliceStable(b.Sizes, func(i, j int) bool { return b.Sizes[i].Position < b.Sizes[j].Position })
	if b.Groups == nil {
		b.Groups = map[uuid.UUID]menu.BankGroup{}
	}
	for _, m := range f.menus {
		bm := menu.BankMenu{Menu: m, Categories: []menu.BankCategory{}}
		for _, c := range f.cats {
			if c.MenuID != m.ID {
				continue
			}
			bc := menu.BankCategory{Category: c, Items: []menu.BankItem{}}
			for _, it := range f.items {
				if it.CategoryID == c.ID {
					bc.Items = append(bc.Items, clone(it))
				}
			}
			sort.SliceStable(bc.Items, func(i, j int) bool { return bc.Items[i].Position < bc.Items[j].Position })
			bm.Categories = append(bm.Categories, bc)
		}
		sort.SliceStable(bm.Categories, func(i, j int) bool { return bm.Categories[i].Position < bm.Categories[j].Position })
		b.Menus = append(b.Menus, bm)
	}
	if b.Menus == nil {
		b.Menus = []menu.BankMenu{}
	}
	sort.SliceStable(b.Menus, func(i, j int) bool { return b.Menus[i].Position < b.Menus[j].Position })
	return b, nil
}

func (f *fakeStore) Printers(context.Context) ([]bankdoc.Printer, error) { return nil, nil }
func (f *fakeStore) Views(context.Context) ([]menuview.View, error)      { return clone(f.views), nil }
func (f *fakeStore) Entries(_ context.Context, v uuid.UUID) ([]menuview.Entry, error) {
	return clone(f.entries[v]), nil
}
func (f *fakeStore) Deals(context.Context) ([]pricing.StoreDeal, error) { return clone(f.deals), nil }

func (f *fakeStore) CreateSize(_ context.Context, name, short string, pos int) (uuid.UUID, error) {
	id := f.id()
	f.sizes = append(f.sizes, menu.Size{ID: id, Name: name, Short: short, Position: pos})
	return id, nil
}
func (f *fakeStore) UpdateSize(_ context.Context, id uuid.UUID, name, short string, pos int) error {
	f.version++
	for i := range f.sizes {
		if f.sizes[i].ID == id {
			f.sizes[i].Name, f.sizes[i].Short, f.sizes[i].Position = name, short, pos
		}
	}
	return nil
}
func (f *fakeStore) DeleteSize(_ context.Context, id uuid.UUID) error {
	f.version++
	for i := range f.sizes {
		if f.sizes[i].ID == id {
			f.sizes = append(f.sizes[:i], f.sizes[i+1:]...)
			return nil
		}
	}
	return nil
}

func (f *fakeStore) CreateMenu(_ context.Context, m menu.Menu) (uuid.UUID, error) {
	m.ID = f.id()
	f.menus = append(f.menus, m)
	return m.ID, nil
}
func (f *fakeStore) UpdateMenu(_ context.Context, m menu.Menu) error {
	f.version++
	for i := range f.menus {
		if f.menus[i].ID == m.ID {
			f.menus[i] = m
		}
	}
	return nil
}
func (f *fakeStore) DeleteMenu(_ context.Context, id uuid.UUID) error {
	f.version++
	out := f.menus[:0]
	for _, m := range f.menus {
		if m.ID != id {
			out = append(out, m)
		}
	}
	f.menus = out
	return nil
}

func (f *fakeStore) CreateCategory(_ context.Context, c menu.Category) (uuid.UUID, error) {
	c.ID = f.id()
	f.cats = append(f.cats, c)
	return c.ID, nil
}
func (f *fakeStore) UpdateCategory(_ context.Context, c menu.Category) error {
	f.version++
	for i := range f.cats {
		if f.cats[i].ID == c.ID {
			f.cats[i] = c
		}
	}
	return nil
}
func (f *fakeStore) DeleteCategory(_ context.Context, id uuid.UUID) error {
	f.version++
	out := f.cats[:0]
	for _, c := range f.cats {
		if c.ID != id {
			out = append(out, c)
		}
	}
	f.cats = out
	return nil
}

func (f *fakeStore) item(id uuid.UUID) *menu.BankItem {
	for i := range f.items {
		if f.items[i].ID == id {
			return &f.items[i]
		}
	}
	return nil
}
func (f *fakeStore) CreateItem(_ context.Context, it menu.Item) (uuid.UUID, error) {
	it.ID = f.id()
	f.items = append(f.items, menu.BankItem{Item: it, Sizes: []menu.ItemSize{}, GroupIDs: []uuid.UUID{}})
	return it.ID, nil
}
func (f *fakeStore) UpdateItem(_ context.Context, it menu.Item) error {
	f.version++
	if p := f.item(it.ID); p != nil {
		p.Item = it
	}
	return nil
}
func (f *fakeStore) SetItemBank(_ context.Context, id uuid.UUID, x menu.ItemExtra) error {
	f.version++
	if p := f.item(id); p != nil {
		p.KitchenName, p.SKU, p.Sizes = x.KitchenName, x.SKU, x.Sizes
	}
	return nil
}
func (f *fakeStore) SetItemGroups(_ context.Context, id uuid.UUID, groups []uuid.UUID) error {
	f.version++
	if p := f.item(id); p != nil {
		p.GroupIDs = groups
	}
	return nil
}
func (f *fakeStore) DeleteItem(_ context.Context, id uuid.UUID) error {
	f.version++
	out := f.items[:0]
	for _, it := range f.items {
		if it.ID != id {
			out = append(out, it)
		}
	}
	f.items = out
	return nil
}

func (f *fakeStore) CreateGroup(_ context.Context, g bankdoc.GroupRow) (uuid.UUID, error) {
	g.ID = f.id()
	f.groups[g.ID] = menu.BankGroup{ModifierGroup: g.ModifierGroup, Portions: g.Portions, Options: []menu.BankOption{}}
	return g.ID, nil
}
func (f *fakeStore) UpdateGroup(_ context.Context, g bankdoc.GroupRow) error {
	f.version++
	cur := f.groups[g.ID]
	cur.ModifierGroup, cur.Portions = g.ModifierGroup, g.Portions
	f.groups[g.ID] = cur
	return nil
}
func (f *fakeStore) DeleteGroup(_ context.Context, id uuid.UUID) error {
	f.version++
	delete(f.groups, id)
	return nil
}
func (f *fakeStore) CreateOption(_ context.Context, o bankdoc.OptionRow) (uuid.UUID, error) {
	o.ID = f.id()
	g := f.groups[o.GroupID]
	g.Options = append(g.Options, menu.BankOption{ModifierOption: o.ModifierOption, IsDefault: o.IsDefault, KitchenName: o.KitchenName,
		Prices: []menu.OptionPrice{}, ShowOnDriverTicket: o.ShowOnDriverTicket})
	f.groups[o.GroupID] = g
	return o.ID, nil
}
func (f *fakeStore) UpdateOption(_ context.Context, o bankdoc.OptionRow) error {
	f.version++
	g := f.groups[o.GroupID]
	for i := range g.Options {
		if g.Options[i].ID == o.ID {
			prices := g.Options[i].Prices
			g.Options[i] = menu.BankOption{ModifierOption: o.ModifierOption, IsDefault: o.IsDefault, KitchenName: o.KitchenName,
				Prices: prices, ShowOnDriverTicket: o.ShowOnDriverTicket, NeverIncluded: g.Options[i].NeverIncluded}
		}
	}
	sort.SliceStable(g.Options, func(i, j int) bool { return g.Options[i].Position < g.Options[j].Position })
	f.groups[o.GroupID] = g
	return nil
}
func (f *fakeStore) SetOptionNeverIncluded(_ context.Context, id uuid.UUID, on bool) error {
	f.version++
	for gid, g := range f.groups {
		for i := range g.Options {
			if g.Options[i].ID == id {
				g.Options[i].NeverIncluded = on
			}
		}
		f.groups[gid] = g
	}
	return nil
}
func (f *fakeStore) SetOptionPrices(_ context.Context, id uuid.UUID, prices []menu.OptionPrice) error {
	f.version++
	for gid, g := range f.groups {
		for i := range g.Options {
			if g.Options[i].ID == id {
				g.Options[i].Prices = prices
			}
		}
		f.groups[gid] = g
	}
	return nil
}
func (f *fakeStore) DeleteOption(_ context.Context, id uuid.UUID) error {
	f.version++
	for gid, g := range f.groups {
		out := []menu.BankOption{}
		for _, o := range g.Options {
			if o.ID != id {
				out = append(out, o)
			}
		}
		g.Options = out
		f.groups[gid] = g
	}
	return nil
}

func (f *fakeStore) CreateView(_ context.Context, kind menuview.Kind, name string, rules menuview.Rules) (uuid.UUID, error) {
	id := f.id()
	f.views = append(f.views, menuview.View{ID: id, Kind: kind, Name: name, Rules: rules})
	return id, nil
}
func (f *fakeStore) UpdateView(_ context.Context, id uuid.UUID, name string, rules menuview.Rules) error {
	for i := range f.views {
		if f.views[i].ID == id {
			f.views[i].Name, f.views[i].Rules = name, rules
		}
	}
	return nil
}
func (f *fakeStore) ReplaceEntries(_ context.Context, v menuview.View, entries []menuview.Entry) error {
	f.entries[v.ID] = clone(entries)
	return nil
}
func (f *fakeStore) Layout(_ context.Context, id uuid.UUID) (*menuview.Layout, error) {
	return clone(f.layouts[id]), nil
}
func (f *fakeStore) SetLayout(_ context.Context, id uuid.UUID, l *menuview.Layout) error {
	f.layouts[id] = clone(l)
	for i := range f.views {
		if f.views[i].ID == id {
			f.views[i].HasLayout = true
		}
	}
	return nil
}

func (f *fakeStore) CreateFamily(context.Context, menu.Family) (uuid.UUID, error) { return f.id(), nil }
func (f *fakeStore) UpdateFamily(context.Context, menu.Family) error              { return nil }
func (f *fakeStore) DeleteFamily(context.Context, uuid.UUID) error                { return nil }
func (f *fakeStore) SetFamily(context.Context, bool, []uuid.UUID, *uuid.UUID) error {
	return nil
}

func (f *fakeStore) CreateDeal(_ context.Context, d pricing.StoreDeal) error {
	d.ID = f.id().String()
	f.deals = append(f.deals, d)
	return nil
}
func (f *fakeStore) UpdateDeal(_ context.Context, id uuid.UUID, d pricing.StoreDeal) error {
	for i := range f.deals {
		if f.deals[i].ID == id.String() {
			d.ID = id.String()
			f.deals[i] = d
		}
	}
	return nil
}
func (f *fakeStore) DeleteDeal(_ context.Context, id uuid.UUID) error {
	out := f.deals[:0]
	for _, d := range f.deals {
		if d.ID != id.String() {
			out = append(out, d)
		}
	}
	f.deals = out
	return nil
}

func testService(f *fakeStore) *Service { return NewService(f, zerolog.Nop()) }
