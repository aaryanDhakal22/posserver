package bankimport

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"

	"quiccpos/main/internal/domain/bankdoc"
	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/pricing"

	"github.com/google/uuid"
)

// Included toppings (the owner, 2026-11-13): an item's `included_toppings`
// (its attribute included_toppings) and its sizes' own counts (attribute
// included_toppings_sizes, size id → count), and an option's
// `never_included` (00009). Applied after the tree and the half bases, on
// the bank as it now is.

// applyIncluded sets each document item's included toppings (nil = keep; a
// document item that names sizes replaces the per-size counts) and each
// document option's never_included (nil = keep).
func (a *applier) applyIncluded(ctx context.Context) error {
	bank, err := a.tx.Bank(ctx)
	if err != nil {
		return err
	}
	cur := map[uuid.UUID]menu.BankItem{}
	for _, m := range bank.Menus {
		for _, c := range m.Categories {
			for _, it := range c.Items {
				cur[it.ID] = it
			}
		}
	}
	for i, dm := range a.doc.Menus {
		for j, dc := range dm.Categories {
			for k, di := range dc.Items {
				path := fmt.Sprintf("menus[%d].categories[%d].items[%d]", i, j, k)
				perSize := map[string]int{}
				named := di.Sizes != nil
				if named {
					for z, ds := range *di.Sizes {
						if ds.IncludedToppings == nil {
							continue
						}
						n := *ds.IncludedToppings
						if n < 0 || n > pricing.MaxIncluded {
							return bad(fmt.Sprintf("%s.sizes[%d].included_toppings", path, z), "0 to %d", pricing.MaxIncluded)
						}
						zid, err := a.sizeID(fmt.Sprintf("%s.sizes[%d].size", path, z), ds.Size)
						if err != nil {
							return err
						}
						perSize[zid.String()] = n
					}
				}
				if di.IncludedToppings == nil && !named {
					continue
				}
				if di.IncludedToppings != nil && (*di.IncludedToppings < 0 || *di.IncludedToppings > pricing.MaxIncluded) {
					return bad(path+".included_toppings", "0 to %d", pricing.MaxIncluded)
				}
				it, ok := cur[a.items[bankdoc.Norm(di.Name)]]
				if !ok {
					continue
				}
				n0, sizes0 := pricing.IncludedOf(it.Attributes)
				attrs := maps.Clone(it.Attributes)
				if attrs == nil {
					attrs = map[string]any{}
				}
				changed := []string{}
				if di.IncludedToppings != nil && *di.IncludedToppings != n0 {
					if *di.IncludedToppings == 0 {
						delete(attrs, pricing.IncludedAttr)
					} else {
						attrs[pricing.IncludedAttr] = *di.IncludedToppings
					}
					changed = append(changed, "included_toppings")
				}
				if named && !maps.Equal(perSize, sizes0) && (len(perSize) > 0 || len(sizes0) > 0) {
					if len(perSize) == 0 {
						delete(attrs, pricing.IncludedSizesAttr)
					} else {
						m := map[string]any{}
						for z, n := range perSize {
							m[z] = n
						}
						attrs[pricing.IncludedSizesAttr] = m
					}
					changed = append(changed, "size included_toppings")
				}
				if len(changed) == 0 {
					continue
				}
				upd := it.Item
				upd.Attributes = attrs
				what := fmt.Sprintf("item %q", it.Name)
				if err := a.tx.UpdateItem(ctx, upd); err != nil {
					return fmt.Errorf("%s: %w", what, err)
				}
				a.attrsChanged(what, strings.Join(changed, ", "))
			}
		}
	}
	// Options: never_included, by group + option name (the groups applied
	// already, so every document option has its row).
	opt := map[string]menu.BankOption{}
	for _, g := range bank.Groups {
		for _, o := range g.Options {
			opt[bankdoc.Norm(g.Name)+"\x00"+bankdoc.Norm(o.Name)] = o
		}
	}
	for _, dg := range a.doc.Groups {
		for _, do := range dg.Options {
			if do.NeverIncluded == nil {
				continue
			}
			o, ok := opt[bankdoc.Norm(dg.Name)+"\x00"+bankdoc.Norm(do.Name)]
			if !ok || o.NeverIncluded == *do.NeverIncluded {
				continue
			}
			if err := a.tx.SetOptionNeverIncluded(ctx, o.ID, *do.NeverIncluded); err != nil {
				return err
			}
			what := fmt.Sprintf("option %q of %q", strings.TrimSpace(do.Name), strings.TrimSpace(dg.Name))
			a.optionFlagChanged(what, "never_included")
		}
	}
	return nil
}

// optionFlagChanged records an option flag change on an option the groups
// step counted (unchanged becomes updated, once).
func (a *applier) optionFlagChanged(what, field string) {
	seen := false
	for _, ch := range a.res.Changes {
		seen = seen || strings.HasPrefix(ch, what+": ")
	}
	if !seen {
		c := a.res.Counts["options"]
		if c.Unchanged > 0 {
			c.Unchanged--
		}
		c.Updated++
	}
	a.res.Changes = append(a.res.Changes, what+": "+field)
}

// exportIncluded writes each item's included toppings (item and per size)
// and each never-included option (doc items and groups are in the bank's
// order; the groups by name).
func exportIncluded(bank *menu.Bank, doc *bankdoc.Document) {
	size := map[string]string{}
	for _, z := range bank.Sizes {
		size[z.ID.String()] = z.Name
	}
	for i, m := range bank.Menus {
		for j, c := range m.Categories {
			for k, it := range c.Items {
				n, sizes := pricing.IncludedOf(it.Attributes)
				di := &doc.Menus[i].Categories[j].Items[k]
				if n > 0 {
					di.IncludedToppings = &n
				}
				if len(sizes) == 0 || di.Sizes == nil {
					continue
				}
				for z := range *di.Sizes {
					ds := &(*di.Sizes)[z]
					for id, c := range sizes {
						if size[id] == ds.Size {
							c := c
							ds.IncludedToppings = &c
						}
					}
				}
			}
		}
	}
	never := map[string]bool{}
	gids := make([]uuid.UUID, 0, len(bank.Groups))
	for id := range bank.Groups {
		gids = append(gids, id)
	}
	sort.Slice(gids, func(i, j int) bool { return gids[i].String() < gids[j].String() })
	for _, id := range gids {
		g := bank.Groups[id]
		for _, o := range g.Options {
			if o.NeverIncluded {
				never[bankdoc.Norm(g.Name)+"\x00"+bankdoc.Norm(o.Name)] = true
			}
		}
	}
	for gi := range doc.Groups {
		for oi := range doc.Groups[gi].Options {
			o := &doc.Groups[gi].Options[oi]
			if never[bankdoc.Norm(doc.Groups[gi].Name)+"\x00"+bankdoc.Norm(o.Name)] {
				on := true
				o.NeverIncluded = &on
			}
		}
	}
}
