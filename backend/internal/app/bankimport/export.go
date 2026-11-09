package bankimport

import (
	"context"
	"sort"

	"quiccpos/main/internal/domain/bankdoc"
	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/menuview"
	"quiccpos/main/internal/domain/pricing"

	"github.com/google/uuid"
)

// Export writes the location's bank, views and deals as a document. Applying
// it back to the same store changes nothing (while every name is unique).
func (s *Service) Export(ctx context.Context, tenantID, locationID uuid.UUID) (*bankdoc.Document, error) {
	var doc *bankdoc.Document
	err := s.repo.InTx(ctx, tenantID, locationID, func(tx bankdoc.Tx) error {
		var err error
		doc, err = export(ctx, tx)
		if err != nil {
			return err
		}
		return bankdoc.ErrRollback // read only
	})
	return doc, err
}

func export(ctx context.Context, tx bankdoc.Tx) (*bankdoc.Document, error) {
	bank, err := tx.Bank(ctx)
	if err != nil {
		return nil, err
	}
	ps, err := tx.Printers(ctx)
	if err != nil {
		return nil, err
	}
	printer := map[uuid.UUID]string{}
	for _, p := range ps {
		printer[p.ID] = p.Name
	}
	size := map[uuid.UUID]string{}
	doc := &bankdoc.Document{Format: bankdoc.Format, Sizes: []bankdoc.Size{}, Groups: []bankdoc.Group{}, Menus: []bankdoc.Menu{},
		Views: []bankdoc.View{}, Deals: []bankdoc.Deal{}}
	for _, z := range bank.Sizes {
		size[z.ID] = z.Name
		doc.Sizes = append(doc.Sizes, bankdoc.Size{Name: z.Name, Short: z.Short})
	}
	gids := make([]uuid.UUID, 0, len(bank.Groups))
	for id := range bank.Groups {
		gids = append(gids, id)
	}
	sort.Slice(gids, func(i, j int) bool {
		gi, gj := bank.Groups[gids[i]], bank.Groups[gids[j]]
		if !gi.CreatedAt.Equal(gj.CreatedAt) {
			return gi.CreatedAt.Before(gj.CreatedAt)
		}
		return gids[i].String() < gids[j].String()
	})
	group := map[uuid.UUID]string{}
	for _, id := range gids {
		g := bank.Groups[id]
		group[id] = g.Name
		dg := bankdoc.Group{Name: g.Name, Required: g.Required, MinSelect: g.MinSelect, MaxSelect: g.MaxSelect,
			AllowMultiple: g.AllowMultiple, Portions: g.Portions, Options: []bankdoc.Option{}}
		opts := append([]menu.BankOption{}, g.Options...)
		sort.SliceStable(opts, func(i, j int) bool { return opts[i].Position < opts[j].Position })
		for _, o := range opts {
			inStock := o.InStock
			do := bankdoc.Option{Name: o.Name, PriceCents: o.PriceDeltaCents, InStock: &inStock, IsDefault: o.IsDefault, KitchenName: o.KitchenName}
			if len(o.Prices) > 0 {
				do.Prices = map[string]int64{}
				for _, p := range o.Prices {
					do.Prices[size[p.SizeID]] = p.PriceCents
				}
			}
			dg.Options = append(dg.Options, do)
		}
		doc.Groups = append(doc.Groups, dg)
	}
	item, cat := map[string]string{}, map[string]string{}
	for _, m := range bank.Menus {
		avail := m.Available
		dm := bankdoc.Menu{Name: m.Name, Description: m.Description, Available: &avail, Categories: []bankdoc.Category{}}
		for _, c := range m.Categories {
			cat[c.ID.String()] = c.Name
			ca := c.Available
			pn := ""
			if c.DefaultPrinterID != nil {
				pn = printer[*c.DefaultPrinterID]
			}
			dc := bankdoc.Category{Name: c.Name, Description: c.Description, Available: &ca, Printer: &pn, Items: []bankdoc.Item{}}
			for _, it := range c.Items {
				item[it.ID.String()] = it.Name
				in := it.InStock
				ipn := ""
				if it.PrinterID != nil {
					ipn = printer[*it.PrinterID]
				}
				tags := append([]string{}, it.Tags...)
				sizes := make([]bankdoc.ItemSize, 0, len(it.Sizes))
				for _, z := range it.Sizes {
					sizes = append(sizes, bankdoc.ItemSize{Size: size[z.SizeID], PriceCents: z.PriceCents, KitchenName: z.KitchenName})
				}
				groups := make([]string, 0, len(it.GroupIDs))
				for _, g := range it.GroupIDs {
					groups = append(groups, group[g])
				}
				dc.Items = append(dc.Items, bankdoc.Item{Name: it.Name, Description: it.Description, PriceCents: it.PriceCents,
					TaxRateBps: it.TaxRateBps, InStock: &in, Tags: &tags, Printer: &ipn, KitchenName: it.KitchenName, SKU: it.SKU,
					Sizes: &sizes, Groups: &groups})
			}
			dm.Categories = append(dm.Categories, dc)
		}
		doc.Menus = append(doc.Menus, dm)
	}
	views, err := tx.Views(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range views {
		dv := bankdoc.View{Kind: string(v.Kind), Name: v.Name, Rules: bankdoc.Rules{MarkupBps: v.Rules.MarkupBps, Round: v.Rules.Round,
			RequirePhoto: v.Rules.RequirePhoto, HiddenCategories: []string{}}, Entries: []bankdoc.Entry{}}
		for _, id := range v.Rules.HiddenCategories {
			dv.Rules.HiddenCategories = append(dv.Rules.HiddenCategories, nameOr(cat, id.String()))
		}
		entries, err := tx.Entries(ctx, v.ID)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			dv.Entries = append(dv.Entries, exportEntry(e, item, size))
		}
		doc.Views = append(doc.Views, dv)
	}
	deals, err := tx.Deals(ctx)
	if err != nil {
		return nil, err
	}
	for _, d := range deals {
		names := func(e *pricing.Eligible) {
			if e == nil {
				return
			}
			for i, id := range e.Items {
				e.Items[i] = nameOr(item, id)
			}
			for i, id := range e.Categories {
				e.Categories[i] = nameOr(cat, id)
			}
			for i, id := range e.Sizes {
				if u, err := uuid.Parse(id); err == nil && size[u] != "" {
					e.Sizes[i] = size[u]
				}
			}
		}
		names(d.Eligible)
		for i := range d.Slots {
			names(&d.Slots[i].Eligible)
		}
		d.ID = ""
		d.Position = 0
		d.CreatedAt, d.UpdatedAt = zeroTime, zeroTime
		doc.Deals = append(doc.Deals, d)
	}
	return doc, nil
}

func nameOr(m map[string]string, id string) string {
	if n, ok := m[id]; ok {
		return n
	}
	return id
}

func exportEntry(e menuview.Entry, item map[string]string, size map[uuid.UUID]string) bankdoc.Entry {
	pos := e.Position
	de := bankdoc.Entry{Kind: e.Kind, Items: []string{}, DisplayName: e.DisplayName, Description: e.Description, Photo: e.Photo,
		Category: e.Category, Position: &pos, Hidden: e.Hidden, PriceOverrideCents: e.PriceOverrideCents,
		VendorName: e.Extra.VendorName, VendorSize: e.Extra.VendorSize}
	for _, id := range e.Anchor.ItemIDs {
		de.Items = append(de.Items, nameOr(item, id.String()))
	}
	if e.Anchor.SizeID != nil {
		de.Size = size[*e.Anchor.SizeID]
	}
	if len(e.Extra.SizePrices) > 0 {
		de.SizePrices = map[string]int64{}
		for k, c := range e.Extra.SizePrices {
			if u, err := uuid.Parse(k); err == nil && size[u] != "" {
				de.SizePrices[size[u]] = c
			}
		}
	}
	if len(e.Extra.Labels) > 0 {
		de.Labels = map[string]string{}
		for k, l := range e.Extra.Labels {
			de.Labels[nameOr(item, k)] = l
		}
	}
	return de
}
