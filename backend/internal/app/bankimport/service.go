// Package bankimport applies a bank document (domain/bankdoc) to a location
// in one transaction — or, as a dry run, applies it and rolls it back — and
// exports a location's bank, views and deals as a document. It is how the
// weborder menu converter fills a store's bank (WEBORDER-PLAN.md stage 1) and
// how later stores are onboarded from a file.
package bankimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	menuviewApp "quiccpos/main/internal/app/menuview"
	"quiccpos/main/internal/domain/bankdoc"
	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/menuview"
	"quiccpos/main/internal/domain/pricing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// ErrBadDocument is a document the import refuses (nothing is written).
var ErrBadDocument = errors.New("bad bank document")

// BadDocError says where in the document the problem is.
type BadDocError struct {
	Path string
	Msg  string
}

func (e *BadDocError) Error() string { return e.Path + ": " + e.Msg }
func (e *BadDocError) Unwrap() error { return ErrBadDocument }

func bad(path, format string, a ...any) error {
	return &BadDocError{Path: path, Msg: fmt.Sprintf(format, a...)}
}

// Repo runs the import's transaction (repositories.BankImportRepository).
type Repo interface {
	InTx(ctx context.Context, tenantID, locationID uuid.UUID, fn func(bankdoc.Tx) error) error
}

// Options of an apply.
type Options struct {
	DryRun bool // apply, report, roll back
	Prune  bool // delete what the document doesn't name (never views)
}

// Counts are one kind's changes.
type Counts struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Deleted   int `json:"deleted"`
	Unchanged int `json:"unchanged"`
}

// Result is what an apply did (or, in a dry run, would do).
type Result struct {
	DryRun         bool                                 `json:"dry_run"`
	Applied        bool                                 `json:"applied"`
	Version        int64                                `json:"version"` // the menu version after (a dry run: as it would be)
	Counts         map[string]*Counts                   `json:"counts"`
	Changes        []string                             `json:"changes"`
	Warnings       []string                             `json:"warnings"`
	NeedsAttention map[string][]menuview.NeedsAttention `json:"needs_attention"` // "<kind>:<view name>"
}

// Kinds counted in a Result.
var kinds = []string{"sizes", "groups", "options", "menus", "categories", "items", "views", "entries", "deals"}

type Service struct {
	repo   Repo
	logger zerolog.Logger
}

func NewService(repo Repo, lg zerolog.Logger) *Service {
	return &Service{repo: repo, logger: lg.With().Str("component", "bankimport").Logger()}
}

// Apply applies doc to the location.
func (s *Service) Apply(ctx context.Context, tenantID, locationID uuid.UUID, doc *bankdoc.Document, o Options) (*Result, error) {
	if err := checkShape(doc); err != nil {
		return nil, err
	}
	res := &Result{DryRun: o.DryRun, Counts: map[string]*Counts{}, Changes: []string{}, Warnings: []string{},
		NeedsAttention: map[string][]menuview.NeedsAttention{}}
	for _, k := range kinds {
		res.Counts[k] = &Counts{}
	}
	err := s.repo.InTx(ctx, tenantID, locationID, func(tx bankdoc.Tx) error {
		a := &applier{tx: tx, doc: doc, o: o, res: res}
		if err := a.run(ctx); err != nil {
			return err
		}
		if o.DryRun {
			return bankdoc.ErrRollback
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	res.Applied = !o.DryRun
	if res.Applied {
		s.logger.Info().Str("location_id", locationID.String()).Int64("version", res.Version).Int("changes", len(res.Changes)).
			Bool("prune", o.Prune).Msg("bank document applied")
	}
	return res, nil
}

// checkShape checks what needs no database: format, names, uniqueness, prices.
func checkShape(doc *bankdoc.Document) error {
	if doc == nil {
		return bad("document", "empty")
	}
	if doc.Format != bankdoc.Format {
		return bad("format", "must be %d", bankdoc.Format)
	}
	uniq := func(path, kind string, seen map[string]string, name string) error {
		n := bankdoc.Norm(name)
		if n == "" {
			return bad(path, "%s name is required", kind)
		}
		if prev, ok := seen[n]; ok {
			return bad(path, "%s %q is already at %s", kind, name, prev)
		}
		seen[n] = path
		return nil
	}
	sizes := map[string]string{}
	for i, z := range doc.Sizes {
		if err := uniq(fmt.Sprintf("sizes[%d]", i), "size", sizes, z.Name); err != nil {
			return err
		}
	}
	groups := map[string]string{}
	for i, g := range doc.Groups {
		p := fmt.Sprintf("groups[%d]", i)
		if err := uniq(p, "group", groups, g.Name); err != nil {
			return err
		}
		if g.MinSelect < 0 || g.MaxSelect < 0 || (g.MaxSelect > 0 && g.MinSelect > g.MaxSelect) || (g.Required && g.MinSelect < 1) {
			return bad(p, "min/max select don't fit (required needs min_select ≥ 1)")
		}
		opts := map[string]string{}
		for j, op := range g.Options {
			q := fmt.Sprintf("%s.options[%d]", p, j)
			if err := uniq(q, "option", opts, op.Name); err != nil {
				return err
			}
			if op.PriceCents < 0 {
				return bad(q, "price can't be negative")
			}
			for _, c := range op.Prices {
				if c < 0 {
					return bad(q, "price can't be negative")
				}
			}
		}
	}
	menus, cats, items := map[string]string{}, map[string]string{}, map[string]string{}
	for i, m := range doc.Menus {
		p := fmt.Sprintf("menus[%d]", i)
		if err := uniq(p, "menu", menus, m.Name); err != nil {
			return err
		}
		for j, c := range m.Categories {
			q := fmt.Sprintf("%s.categories[%d]", p, j)
			if err := uniq(q, "category", cats, c.Name); err != nil {
				return err
			}
			for k, it := range c.Items {
				r := fmt.Sprintf("%s.items[%d]", q, k)
				if err := uniq(r, "item", items, it.Name); err != nil {
					return err
				}
				if it.PriceCents < 0 || it.TaxRateBps < 0 || it.TaxRateBps > 10000 {
					return bad(r, "price can't be negative; tax_rate_bps is 0..10000")
				}
				if it.Sizes != nil {
					seen := map[string]string{}
					for l, z := range *it.Sizes {
						zp := fmt.Sprintf("%s.sizes[%d]", r, l)
						if err := uniq(zp, "size", seen, z.Size); err != nil {
							return err
						}
						if z.PriceCents < 0 {
							return bad(zp, "price can't be negative")
						}
					}
				}
			}
		}
	}
	views := map[string]string{}
	for i, v := range doc.Views {
		p := fmt.Sprintf("views[%d]", i)
		if !menuview.Kind(v.Kind).Valid() {
			return bad(p, "kind must be web, operator, counter or vendor")
		}
		if err := uniq(p, "view", views, v.Kind+"|"+v.Name); err != nil {
			return err
		}
		if strings.TrimSpace(v.Name) == "" {
			return bad(p, "view name is required")
		}
	}
	deals := map[string]string{}
	for i, d := range doc.Deals {
		if err := uniq(fmt.Sprintf("deals[%d]", i), "deal", deals, d.Name); err != nil {
			return err
		}
	}
	return nil
}

// --- the apply -----------------------------------------------------------------------

type applier struct {
	tx  bankdoc.Tx
	doc *bankdoc.Document
	o   Options
	res *Result

	bank     *menu.Bank
	printers map[string]uuid.UUID // norm name → id
	sizes    map[string]uuid.UUID // norm name → id (the document's first, then the bank's)
	groups   map[string]uuid.UUID
	menus    map[string]uuid.UUID
	cats     map[string]uuid.UUID
	items    map[string]uuid.UUID
}

func (a *applier) change(kind, what string, created bool, fields []string) {
	c := a.res.Counts[kind]
	switch {
	case created:
		c.Created++
		a.res.Changes = append(a.res.Changes, what+": created")
	case len(fields) > 0:
		c.Updated++
		a.res.Changes = append(a.res.Changes, what+": "+strings.Join(fields, ", "))
	default:
		c.Unchanged++
	}
}

func (a *applier) deleted(kind, what string) {
	a.res.Counts[kind].Deleted++
	a.res.Changes = append(a.res.Changes, what+": deleted")
}

func (a *applier) warn(format string, args ...any) {
	a.res.Warnings = append(a.res.Warnings, fmt.Sprintf(format, args...))
}

func (a *applier) run(ctx context.Context) error {
	var err error
	if a.bank, err = a.tx.Bank(ctx); err != nil {
		return err
	}
	ps, err := a.tx.Printers(ctx)
	if err != nil {
		return err
	}
	a.printers = map[string]uuid.UUID{}
	for _, p := range ps {
		if _, dup := a.printers[bankdoc.Norm(p.Name)]; !dup {
			a.printers[bankdoc.Norm(p.Name)] = p.ID
		}
	}
	steps := []func(context.Context) error{a.applySizes, a.applyGroups, a.applyTree}
	for _, f := range steps {
		if err := f(ctx); err != nil {
			return err
		}
	}
	// Views and deals refer to the bank as it now is.
	if a.bank, err = a.tx.Bank(ctx); err != nil {
		return err
	}
	a.refsFromBank()
	if err := a.applyViews(ctx); err != nil {
		return err
	}
	if err := a.applyDeals(ctx); err != nil {
		return err
	}
	return a.attention(ctx)
}

// --- sizes ---

func (a *applier) applySizes(ctx context.Context) error {
	have := map[string]menu.Size{}
	for _, z := range a.bank.Sizes {
		if _, dup := have[bankdoc.Norm(z.Name)]; !dup {
			have[bankdoc.Norm(z.Name)] = z
		}
	}
	a.sizes = map[string]uuid.UUID{}
	for i, z := range a.doc.Sizes {
		n := bankdoc.Norm(z.Name)
		name, short := strings.TrimSpace(z.Name), strings.TrimSpace(z.Short)
		what := fmt.Sprintf("size %q", name)
		if cur, ok := have[n]; ok {
			var f []string
			f = diff(f, "name", cur.Name, name)
			f = diff(f, "short", cur.Short, short)
			f = diff(f, "position", cur.Position, i)
			if len(f) > 0 {
				if err := a.tx.UpdateSize(ctx, cur.ID, name, short, i); err != nil {
					return fmt.Errorf("%s: %w", what, err)
				}
			}
			a.change("sizes", what, false, f)
			a.sizes[n] = cur.ID
			continue
		}
		id, err := a.tx.CreateSize(ctx, name, short, i)
		if err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		a.change("sizes", what, true, nil)
		a.sizes[n] = id
	}
	for _, z := range a.bank.Sizes {
		n := bankdoc.Norm(z.Name)
		if id, ok := a.sizes[n]; ok && id == z.ID {
			continue
		}
		if a.o.Prune {
			if err := a.tx.DeleteSize(ctx, z.ID); err != nil {
				return err
			}
			a.deleted("sizes", fmt.Sprintf("size %q", z.Name))
		} else if _, ok := a.sizes[n]; !ok {
			a.sizes[n] = z.ID // still referable
		}
	}
	return nil
}

func (a *applier) sizeID(path, name string) (uuid.UUID, error) {
	id, ok := a.sizes[bankdoc.Norm(name)]
	if !ok {
		return uuid.Nil, bad(path, "unknown size %q", name)
	}
	return id, nil
}

// --- groups + options ---

func (a *applier) applyGroups(ctx context.Context) error {
	ids := slices.Collect(maps.Keys(a.bank.Groups))
	sort.Slice(ids, func(i, j int) bool {
		gi, gj := a.bank.Groups[ids[i]], a.bank.Groups[ids[j]]
		if !gi.CreatedAt.Equal(gj.CreatedAt) {
			return gi.CreatedAt.Before(gj.CreatedAt)
		}
		return ids[i].String() < ids[j].String()
	})
	have := map[string]menu.BankGroup{}
	for _, id := range ids {
		g := a.bank.Groups[id]
		n := bankdoc.Norm(g.Name)
		if _, dup := have[n]; dup {
			a.warn("two modifier groups are named %q; the document matches the older one", g.Name)
			continue
		}
		have[n] = g
	}
	a.groups = map[string]uuid.UUID{}
	for i, dg := range a.doc.Groups {
		path := fmt.Sprintf("groups[%d]", i)
		n := bankdoc.Norm(dg.Name)
		name := strings.TrimSpace(dg.Name)
		what := fmt.Sprintf("group %q", name)
		row := bankdoc.GroupRow{ModifierGroup: menu.ModifierGroup{Name: name, Required: dg.Required, MinSelect: dg.MinSelect,
			MaxSelect: dg.MaxSelect, AllowMultiple: dg.AllowMultiple}, Portions: dg.Portions}
		cur, ok := have[n]
		var gid uuid.UUID
		if ok {
			gid = cur.ID
			row.ID = gid
			var f []string
			f = diff(f, "name", cur.Name, name)
			f = diff(f, "required", cur.Required, dg.Required)
			f = diff(f, "min_select", cur.MinSelect, dg.MinSelect)
			f = diff(f, "max_select", cur.MaxSelect, dg.MaxSelect)
			f = diff(f, "allow_multiple", cur.AllowMultiple, dg.AllowMultiple)
			f = diff(f, "portions", cur.Portions, dg.Portions)
			if len(f) > 0 {
				if err := a.tx.UpdateGroup(ctx, row); err != nil {
					return fmt.Errorf("%s: %w", what, err)
				}
			}
			a.change("groups", what, false, f)
		} else {
			id, err := a.tx.CreateGroup(ctx, row)
			if err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
			gid = id
			a.change("groups", what, true, nil)
		}
		a.groups[n] = gid
		if err := a.applyOptions(ctx, path, name, gid, cur.Options, dg.Options); err != nil {
			return err
		}
	}
	for _, id := range ids {
		g := a.bank.Groups[id]
		n := bankdoc.Norm(g.Name)
		if got, ok := a.groups[n]; ok && got == id {
			continue
		}
		if a.o.Prune {
			if err := a.tx.DeleteGroup(ctx, id); err != nil {
				return err
			}
			a.deleted("groups", fmt.Sprintf("group %q", g.Name))
		} else if _, ok := a.groups[n]; !ok {
			a.groups[n] = id
		}
	}
	return nil
}

func (a *applier) applyOptions(ctx context.Context, gpath, gname string, gid uuid.UUID, cur []menu.BankOption, doc []bankdoc.Option) error {
	have := map[string]menu.BankOption{}
	for _, o := range cur {
		if _, dup := have[bankdoc.Norm(o.Name)]; !dup {
			have[bankdoc.Norm(o.Name)] = o
		}
	}
	kept := map[uuid.UUID]bool{}
	for j, do := range doc {
		path := fmt.Sprintf("%s.options[%d]", gpath, j)
		name := strings.TrimSpace(do.Name)
		what := fmt.Sprintf("option %q of %q", name, gname)
		prices := make([]menu.OptionPrice, 0, len(do.Prices))
		for zn, c := range do.Prices {
			zid, err := a.sizeID(path+".prices", zn)
			if err != nil {
				return err
			}
			prices = append(prices, menu.OptionPrice{SizeID: zid, PriceCents: c})
		}
		row := bankdoc.OptionRow{ModifierOption: menu.ModifierOption{GroupID: gid, Name: name, PriceDeltaCents: do.PriceCents,
			InStock: true, Position: j}, IsDefault: do.IsDefault, KitchenName: strings.TrimSpace(do.KitchenName)}
		c, ok := have[bankdoc.Norm(name)]
		if !ok {
			if do.InStock != nil {
				row.InStock = *do.InStock
			}
			id, err := a.tx.CreateOption(ctx, row)
			if err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
			if len(prices) > 0 {
				if err := a.tx.SetOptionPrices(ctx, id, prices); err != nil {
					return fmt.Errorf("%s: %w", what, err)
				}
			}
			a.change("options", what, true, nil)
			continue
		}
		kept[c.ID] = true
		row.ID = c.ID
		row.InStock = c.InStock
		if do.InStock != nil {
			row.InStock = *do.InStock
		}
		var f []string
		f = diff(f, "name", c.Name, name)
		f = diff(f, "price", c.PriceDeltaCents, do.PriceCents)
		f = diff(f, "in_stock", c.InStock, row.InStock)
		f = diff(f, "position", c.Position, j)
		f = diff(f, "is_default", c.IsDefault, do.IsDefault)
		f = diff(f, "kitchen_name", c.KitchenName, row.KitchenName)
		if len(f) > 0 {
			if err := a.tx.UpdateOption(ctx, row); err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
		}
		if !samePrices(c.Prices, prices) {
			if err := a.tx.SetOptionPrices(ctx, c.ID, prices); err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
			f = append(f, "size prices")
		}
		a.change("options", what, false, f)
	}
	if a.o.Prune {
		for _, o := range cur {
			if !kept[o.ID] {
				if err := a.tx.DeleteOption(ctx, o.ID); err != nil {
					return err
				}
				a.deleted("options", fmt.Sprintf("option %q of %q", o.Name, gname))
			}
		}
	}
	return nil
}

// --- menus → categories → items ---

func (a *applier) printer(path string, name *string, cur *uuid.UUID) (*uuid.UUID, error) {
	if name == nil {
		return cur, nil
	}
	if strings.TrimSpace(*name) == "" {
		return nil, nil
	}
	id, ok := a.printers[bankdoc.Norm(*name)]
	if !ok {
		return nil, bad(path, "unknown printer %q", *name)
	}
	return &id, nil
}

func (a *applier) applyTree(ctx context.Context) error {
	haveMenu := map[string]menu.Menu{}
	haveCat := map[string]menu.Category{}
	haveItem := map[string]menu.BankItem{}
	for _, m := range a.bank.Menus {
		if _, dup := haveMenu[bankdoc.Norm(m.Name)]; dup {
			a.warn("two menus are named %q; the document matches the first", m.Name)
		} else {
			haveMenu[bankdoc.Norm(m.Name)] = m.Menu
		}
		for _, c := range m.Categories {
			if _, dup := haveCat[bankdoc.Norm(c.Name)]; dup {
				a.warn("two categories are named %q; the document matches the first", c.Name)
			} else {
				haveCat[bankdoc.Norm(c.Name)] = c.Category
			}
			for _, it := range c.Items {
				if _, dup := haveItem[bankdoc.Norm(it.Name)]; dup {
					a.warn("two items are named %q; the document matches the first", it.Name)
				} else {
					haveItem[bankdoc.Norm(it.Name)] = it
				}
			}
		}
	}
	a.menus, a.cats, a.items = map[string]uuid.UUID{}, map[string]uuid.UUID{}, map[string]uuid.UUID{}
	for i, dm := range a.doc.Menus {
		name := strings.TrimSpace(dm.Name)
		what := fmt.Sprintf("menu %q", name)
		m := menu.Menu{Name: name, Description: dm.Description, Available: true, Position: i}
		if cur, ok := haveMenu[bankdoc.Norm(name)]; ok {
			m.ID, m.Available = cur.ID, cur.Available
			if dm.Available != nil {
				m.Available = *dm.Available
			}
			var f []string
			f = diff(f, "name", cur.Name, m.Name)
			f = diff(f, "description", cur.Description, m.Description)
			f = diff(f, "available", cur.Available, m.Available)
			f = diff(f, "position", cur.Position, i)
			if len(f) > 0 {
				if err := a.tx.UpdateMenu(ctx, m); err != nil {
					return fmt.Errorf("%s: %w", what, err)
				}
			}
			a.change("menus", what, false, f)
		} else {
			if dm.Available != nil {
				m.Available = *dm.Available
			}
			id, err := a.tx.CreateMenu(ctx, m)
			if err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
			m.ID = id
			a.change("menus", what, true, nil)
		}
		a.menus[bankdoc.Norm(name)] = m.ID
		for j, dc := range dm.Categories {
			if err := a.applyCategory(ctx, fmt.Sprintf("menus[%d].categories[%d]", i, j), m.ID, j, dc, haveCat, haveItem); err != nil {
				return err
			}
		}
	}
	if !a.o.Prune {
		return nil
	}
	for _, m := range a.bank.Menus {
		for _, c := range m.Categories {
			for _, it := range c.Items {
				if id, ok := a.items[bankdoc.Norm(it.Name)]; !ok || id != it.ID {
					if err := a.tx.DeleteItem(ctx, it.ID); err != nil {
						return err
					}
					a.deleted("items", fmt.Sprintf("item %q", it.Name))
				}
			}
			if id, ok := a.cats[bankdoc.Norm(c.Name)]; !ok || id != c.ID {
				if err := a.tx.DeleteCategory(ctx, c.ID); err != nil {
					return err
				}
				a.deleted("categories", fmt.Sprintf("category %q", c.Name))
			}
		}
		if id, ok := a.menus[bankdoc.Norm(m.Name)]; !ok || id != m.ID {
			if err := a.tx.DeleteMenu(ctx, m.ID); err != nil {
				return err
			}
			a.deleted("menus", fmt.Sprintf("menu %q", m.Name))
		}
	}
	return nil
}

func (a *applier) applyCategory(ctx context.Context, path string, menuID uuid.UUID, pos int, dc bankdoc.Category,
	haveCat map[string]menu.Category, haveItem map[string]menu.BankItem) error {
	name := strings.TrimSpace(dc.Name)
	what := fmt.Sprintf("category %q", name)
	c := menu.Category{MenuID: menuID, Name: name, Description: dc.Description, Available: true, Position: pos}
	cur, ok := haveCat[bankdoc.Norm(name)]
	var err error
	if ok {
		c.ID, c.Available = cur.ID, cur.Available
		if dc.Available != nil {
			c.Available = *dc.Available
		}
		if c.DefaultPrinterID, err = a.printer(path+".printer", dc.Printer, cur.DefaultPrinterID); err != nil {
			return err
		}
		var f []string
		f = diff(f, "name", cur.Name, c.Name)
		f = diff(f, "description", cur.Description, c.Description)
		f = diff(f, "available", cur.Available, c.Available)
		f = diff(f, "menu", cur.MenuID, c.MenuID)
		f = diff(f, "position", cur.Position, pos)
		f = diff(f, "printer", idStr(cur.DefaultPrinterID), idStr(c.DefaultPrinterID))
		if len(f) > 0 {
			if err := a.tx.UpdateCategory(ctx, c); err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
		}
		a.change("categories", what, false, f)
	} else {
		if dc.Available != nil {
			c.Available = *dc.Available
		}
		if c.DefaultPrinterID, err = a.printer(path+".printer", dc.Printer, nil); err != nil {
			return err
		}
		if c.ID, err = a.tx.CreateCategory(ctx, c); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		a.change("categories", what, true, nil)
	}
	a.cats[bankdoc.Norm(name)] = c.ID
	for k, di := range dc.Items {
		if err := a.applyItem(ctx, fmt.Sprintf("%s.items[%d]", path, k), c.ID, k, di, haveItem); err != nil {
			return err
		}
	}
	return nil
}

func (a *applier) applyItem(ctx context.Context, path string, catID uuid.UUID, pos int, di bankdoc.Item, haveItem map[string]menu.BankItem) error {
	name := strings.TrimSpace(di.Name)
	what := fmt.Sprintf("item %q", name)
	cur, exists := haveItem[bankdoc.Norm(name)]
	it := menu.Item{CategoryID: catID, Name: name, Description: di.Description, PriceCents: di.PriceCents, TaxRateBps: di.TaxRateBps,
		InStock: true, Tags: []string{}, Position: pos, Attributes: map[string]any{}}
	extra := menu.ItemExtra{KitchenName: strings.TrimSpace(di.KitchenName), SKU: strings.TrimSpace(di.SKU), Sizes: []menu.ItemSize{}}
	var groups []uuid.UUID
	if exists {
		it.ID, it.InStock, it.OutUntil, it.Tags, it.Attributes = cur.ID, cur.InStock, cur.OutUntil, cur.Tags, cur.Attributes
		extra.Sizes, groups = cur.Sizes, cur.GroupIDs
	}
	if di.InStock != nil {
		it.InStock = *di.InStock
	}
	if di.Tags != nil {
		it.Tags = append([]string{}, (*di.Tags)...)
	}
	var err error
	var curPrinter *uuid.UUID
	if exists {
		curPrinter = cur.PrinterID
	}
	if it.PrinterID, err = a.printer(path+".printer", di.Printer, curPrinter); err != nil {
		return err
	}
	if di.Sizes != nil {
		extra.Sizes = make([]menu.ItemSize, 0, len(*di.Sizes))
		for l, z := range *di.Sizes {
			zid, err := a.sizeID(fmt.Sprintf("%s.sizes[%d]", path, l), z.Size)
			if err != nil {
				return err
			}
			extra.Sizes = append(extra.Sizes, menu.ItemSize{SizeID: zid, PriceCents: z.PriceCents, KitchenName: strings.TrimSpace(z.KitchenName), Position: l})
		}
	}
	if di.Groups != nil {
		groups = make([]uuid.UUID, 0, len(*di.Groups))
		for l, gn := range *di.Groups {
			gid, ok := a.groups[bankdoc.Norm(gn)]
			if !ok {
				return bad(fmt.Sprintf("%s.groups[%d]", path, l), "unknown modifier group %q", gn)
			}
			if slices.Contains(groups, gid) {
				return bad(fmt.Sprintf("%s.groups[%d]", path, l), "group %q is listed twice", gn)
			}
			groups = append(groups, gid)
		}
	}
	if !exists {
		if it.ID, err = a.tx.CreateItem(ctx, it); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		if extra.KitchenName != "" || extra.SKU != "" || len(extra.Sizes) > 0 {
			if err := a.tx.SetItemBank(ctx, it.ID, extra); err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
		}
		if len(groups) > 0 {
			if err := a.tx.SetItemGroups(ctx, it.ID, groups); err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
		}
		a.change("items", what, true, nil)
		a.items[bankdoc.Norm(name)] = it.ID
		return nil
	}
	var f []string
	f = diff(f, "name", cur.Name, it.Name)
	f = diff(f, "description", cur.Description, it.Description)
	f = diff(f, "price", cur.PriceCents, it.PriceCents)
	f = diff(f, "tax_rate_bps", cur.TaxRateBps, it.TaxRateBps)
	f = diff(f, "in_stock", cur.InStock, it.InStock)
	f = diff(f, "category", cur.CategoryID, it.CategoryID)
	f = diff(f, "position", cur.Position, it.Position)
	f = diff(f, "printer", idStr(cur.PrinterID), idStr(it.PrinterID))
	if !slices.Equal(cur.Tags, it.Tags) {
		f = append(f, "tags")
	}
	if len(f) > 0 {
		if err := a.tx.UpdateItem(ctx, it); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
	}
	var fb []string
	fb = diff(fb, "kitchen_name", cur.KitchenName, extra.KitchenName)
	fb = diff(fb, "sku", cur.SKU, extra.SKU)
	if !sameSizes(cur.Sizes, extra.Sizes) {
		fb = append(fb, "sizes")
	}
	if len(fb) > 0 {
		if err := a.tx.SetItemBank(ctx, it.ID, extra); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
	}
	if !slices.Equal(cur.GroupIDs, groups) {
		if err := a.tx.SetItemGroups(ctx, it.ID, groups); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		fb = append(fb, "modifier groups")
	}
	a.change("items", what, false, append(f, fb...))
	a.items[bankdoc.Norm(name)] = it.ID
	return nil
}

// refsFromBank lets views and deals also name bank rows the document didn't
// list (kept when not pruning); the document's own names win.
func (a *applier) refsFromBank() {
	for _, m := range a.bank.Menus {
		for _, c := range m.Categories {
			if _, ok := a.cats[bankdoc.Norm(c.Name)]; !ok {
				a.cats[bankdoc.Norm(c.Name)] = c.ID
			}
			for _, it := range c.Items {
				if _, ok := a.items[bankdoc.Norm(it.Name)]; !ok {
					a.items[bankdoc.Norm(it.Name)] = it.ID
				}
			}
		}
	}
}

func (a *applier) itemID(path, name string) (uuid.UUID, error) {
	id, ok := a.items[bankdoc.Norm(name)]
	if !ok {
		return uuid.Nil, bad(path, "unknown item %q", name)
	}
	return id, nil
}

func (a *applier) catID(path, name string) (uuid.UUID, error) {
	id, ok := a.cats[bankdoc.Norm(name)]
	if !ok {
		return uuid.Nil, bad(path, "unknown category %q", name)
	}
	return id, nil
}

// --- views ---

func (a *applier) applyViews(ctx context.Context) error {
	views, err := a.tx.Views(ctx)
	if err != nil {
		return err
	}
	have := map[string]menuview.View{}
	for _, v := range views {
		k := string(v.Kind) + "|" + bankdoc.Norm(v.Name)
		if _, dup := have[k]; !dup {
			have[k] = v
		}
	}
	for i, dv := range a.doc.Views {
		path := fmt.Sprintf("views[%d]", i)
		kind := menuview.Kind(dv.Kind)
		name := strings.TrimSpace(dv.Name)
		what := fmt.Sprintf("view %s %q", kind, name)
		rules := menuview.Rules{MarkupBps: dv.Rules.MarkupBps, Round: dv.Rules.Round, RequirePhoto: dv.Rules.RequirePhoto, HiddenCategories: []uuid.UUID{}}
		for j, cn := range dv.Rules.HiddenCategories {
			id, err := a.catID(fmt.Sprintf("%s.rules.hidden_categories[%d]", path, j), cn)
			if err != nil {
				return err
			}
			rules.HiddenCategories = append(rules.HiddenCategories, id)
		}
		if err := menuviewApp.ValidRules(&rules); err != nil {
			return bad(path+".rules", "%v", err)
		}
		entries := make([]menuview.Entry, 0, len(dv.Entries))
		for j, de := range dv.Entries {
			e, err := a.entry(fmt.Sprintf("%s.entries[%d]", path, j), kind, j, de)
			if err != nil {
				return err
			}
			entries = append(entries, e)
		}
		v, ok := have[string(kind)+"|"+bankdoc.Norm(name)]
		if ok {
			var f []string
			f = diff(f, "name", v.Name, name)
			if !sameRules(v.Rules, rules) {
				f = append(f, "rules")
			}
			if len(f) > 0 {
				if err := a.tx.UpdateView(ctx, v.ID, name, rules); err != nil {
					return fmt.Errorf("%s: %w", what, err)
				}
			}
			old, err := a.tx.Entries(ctx, v.ID)
			if err != nil {
				return err
			}
			if !sameEntries(old, entries) {
				if err := a.tx.ReplaceEntries(ctx, v, entries); err != nil {
					return fmt.Errorf("%s: %w", what, err)
				}
				a.res.Counts["entries"].Deleted += len(old)
				a.res.Counts["entries"].Created += len(entries)
				f = append(f, fmt.Sprintf("entries (%d → %d)", len(old), len(entries)))
			} else {
				a.res.Counts["entries"].Unchanged += len(entries)
			}
			a.change("views", what, false, f)
			continue
		}
		id, err := a.tx.CreateView(ctx, kind, name, rules)
		if err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		v = menuview.View{ID: id, Kind: kind, Name: name, Rules: rules}
		if err := a.tx.ReplaceEntries(ctx, v, entries); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		a.res.Counts["entries"].Created += len(entries)
		a.change("views", what, true, nil)
	}
	return nil
}

func (a *applier) entry(path string, kind menuview.Kind, idx int, de bankdoc.Entry) (menuview.Entry, error) {
	e := menuview.Entry{Kind: de.Kind, DisplayName: de.DisplayName, Description: de.Description, Photo: de.Photo,
		Category: de.Category, Position: idx, Hidden: de.Hidden, PriceOverrideCents: de.PriceOverrideCents,
		Extra: menuview.Extra{VendorName: de.VendorName, VendorSize: de.VendorSize}}
	if de.Position != nil {
		e.Position = *de.Position
	}
	e.Anchor.ItemIDs = make([]uuid.UUID, 0, len(de.Items))
	for j, n := range de.Items {
		id, err := a.itemID(fmt.Sprintf("%s.items[%d]", path, j), n)
		if err != nil {
			return e, err
		}
		e.Anchor.ItemIDs = append(e.Anchor.ItemIDs, id)
	}
	if strings.TrimSpace(de.Size) != "" {
		id, err := a.sizeID(path+".size", de.Size)
		if err != nil {
			return e, err
		}
		e.Anchor.SizeID = &id
	}
	if len(de.SizePrices) > 0 {
		e.Extra.SizePrices = map[string]int64{}
		for n, c := range de.SizePrices {
			id, err := a.sizeID(path+".size_prices", n)
			if err != nil {
				return e, err
			}
			e.Extra.SizePrices[id.String()] = c
		}
	}
	if len(de.Labels) > 0 {
		e.Extra.Labels = map[string]string{}
		for n, l := range de.Labels {
			id, err := a.itemID(path+".labels", n)
			if err != nil {
				return e, err
			}
			e.Extra.Labels[id.String()] = l
		}
	}
	if err := menuviewApp.ValidEntry(a.bank, kind, &e); err != nil {
		return e, bad(path, "%v", err)
	}
	return e, nil
}

// --- deals ---

func (a *applier) eligible(path string, e *pricing.Eligible) error {
	if e == nil {
		return nil
	}
	for i, n := range e.Items {
		id, err := a.itemID(fmt.Sprintf("%s.items[%d]", path, i), n)
		if err != nil {
			return err
		}
		e.Items[i] = id.String()
	}
	for i, n := range e.Categories {
		id, err := a.catID(fmt.Sprintf("%s.categories[%d]", path, i), n)
		if err != nil {
			return err
		}
		e.Categories[i] = id.String()
	}
	for i, n := range e.Sizes {
		id, err := a.sizeID(fmt.Sprintf("%s.sizes[%d]", path, i), n)
		if err != nil {
			return err
		}
		e.Sizes[i] = id.String()
	}
	return nil
}

func cloneDeal(d bankdoc.Deal) pricing.StoreDeal {
	raw, _ := json.Marshal(d)
	var out pricing.StoreDeal
	_ = json.Unmarshal(raw, &out)
	return out
}

func (a *applier) applyDeals(ctx context.Context) error {
	cur, err := a.tx.Deals(ctx)
	if err != nil {
		return err
	}
	have := map[string]pricing.StoreDeal{}
	for _, d := range cur {
		if _, dup := have[bankdoc.Norm(d.Name)]; !dup {
			have[bankdoc.Norm(d.Name)] = d
		}
	}
	kept := map[string]bool{}
	for i, dd := range a.doc.Deals {
		path := fmt.Sprintf("deals[%d]", i)
		d := cloneDeal(dd)
		d.Name = strings.TrimSpace(d.Name)
		if err := a.eligible(path+".eligible", d.Eligible); err != nil {
			return err
		}
		for j := range d.Slots {
			if err := a.eligible(fmt.Sprintf("%s.slots[%d].eligible", path, j), &d.Slots[j].Eligible); err != nil {
				return err
			}
		}
		if err := d.Validate(); err != nil {
			return bad(path, "%v", err)
		}
		if err := d.ValidKinds(); err != nil {
			return bad(path, "%v", err)
		}
		what := fmt.Sprintf("deal %q", d.Name)
		if c, ok := have[bankdoc.Norm(d.Name)]; ok {
			kept[c.ID] = true
			if sameDeal(c, d) {
				a.change("deals", what, false, nil)
				continue
			}
			id, _ := uuid.Parse(c.ID)
			if err := a.tx.UpdateDeal(ctx, id, d); err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
			a.change("deals", what, false, []string{"definition"})
			continue
		}
		if err := a.tx.CreateDeal(ctx, d); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		a.change("deals", what, true, nil)
	}
	if a.o.Prune {
		for _, d := range cur {
			if !kept[d.ID] {
				id, _ := uuid.Parse(d.ID)
				if err := a.tx.DeleteDeal(ctx, id); err != nil {
					return err
				}
				a.deleted("deals", fmt.Sprintf("deal %q", d.Name))
			}
		}
	}
	return nil
}

// --- what the views now say ---

func (a *applier) attention(ctx context.Context) error {
	bank, err := a.tx.Bank(ctx)
	if err != nil {
		return err
	}
	a.res.Version = bank.Version
	views, err := a.tx.Views(ctx)
	if err != nil {
		return err
	}
	for _, dv := range a.doc.Views {
		for _, v := range views {
			if string(v.Kind) != dv.Kind || bankdoc.Norm(v.Name) != bankdoc.Norm(dv.Name) {
				continue
			}
			entries, err := a.tx.Entries(ctx, v.ID)
			if err != nil {
				return err
			}
			b := menuview.Compile(bank, v, entries, nil)
			na := b.NeedsAttention
			if na == nil {
				na = []menuview.NeedsAttention{}
			}
			a.res.NeedsAttention[string(v.Kind)+":"+v.Name] = na
			break
		}
	}
	return nil
}

// --- comparisons ---

var zeroTime time.Time

func diff[T comparable](f []string, name string, cur, next T) []string {
	if cur != next {
		return append(f, name)
	}
	return f
}

func idStr(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func samePrices(a, b []menu.OptionPrice) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[uuid.UUID]int64{}
	for _, p := range a {
		m[p.SizeID] = p.PriceCents
	}
	for _, p := range b {
		if c, ok := m[p.SizeID]; !ok || c != p.PriceCents {
			return false
		}
	}
	return true
}

func sameSizes(a, b []menu.ItemSize) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].SizeID != b[i].SizeID || a[i].PriceCents != b[i].PriceCents || a[i].KitchenName != b[i].KitchenName {
			return false
		}
	}
	return true
}

func sameRules(a, b menuview.Rules) bool {
	return a.MarkupBps == b.MarkupBps && a.Round == b.Round && a.RequirePhoto == b.RequirePhoto &&
		slices.Equal(a.HiddenCategories, b.HiddenCategories)
}

// sameEntries compares what an entry says, ignoring ids and times.
func sameEntries(a, b []menuview.Entry) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(e menuview.Entry) string {
		e.ID, e.ViewID = uuid.Nil, uuid.Nil
		e.CreatedAt, e.UpdatedAt = zeroTime, zeroTime
		if len(e.Extra.SizePrices) == 0 {
			e.Extra.SizePrices = nil
		}
		if len(e.Extra.Labels) == 0 {
			e.Extra.Labels = nil
		}
		raw, _ := json.Marshal(e)
		return string(raw)
	}
	for i := range a {
		if key(a[i]) != key(b[i]) {
			return false
		}
	}
	return true
}

// sameDeal compares what a deal says, ignoring ids, positions and times.
func sameDeal(a, b pricing.StoreDeal) bool {
	norm := func(d pricing.StoreDeal) string {
		d.ID, d.Position = "", 0
		d.CreatedAt, d.UpdatedAt = zeroTime, zeroTime
		raw, _ := json.Marshal(d)
		return string(raw)
	}
	return norm(a) == norm(b)
}
