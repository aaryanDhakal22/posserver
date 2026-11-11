package bankimport

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"quiccpos/main/internal/domain/bankdoc"
	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/pricing"

	"github.com/google/uuid"
)

// Half and half (TERMINAL-PLAN Stage 0b): an item's `half_base` names its
// plain pizza, kept as the item's half_base_item attribute (the plain's id).
// It is applied once every item exists, so it may name an item further on.

// applyHalfBases sets each document item's plain pizza (nil = keep, "" =
// none); it may name any item the document or the bank has.
func (a *applier) applyHalfBases(ctx context.Context) error {
	cur := map[uuid.UUID]menu.BankItem{}
	for _, m := range a.bank.Menus {
		for _, c := range m.Categories {
			for _, it := range c.Items {
				cur[it.ID] = it
			}
		}
	}
	for i, dm := range a.doc.Menus {
		for j, dc := range dm.Categories {
			for k, di := range dc.Items {
				if di.HalfBase == nil {
					continue
				}
				path := fmt.Sprintf("menus[%d].categories[%d].items[%d].half_base", i, j, k)
				it, ok := cur[a.items[bankdoc.Norm(di.Name)]]
				if !ok {
					continue
				}
				want := ""
				if n := strings.TrimSpace(*di.HalfBase); n != "" {
					id, err := a.itemID(path, n)
					if err != nil {
						return err
					}
					if id == it.ID {
						return bad(path, "an item can't be its own plain pizza")
					}
					want = id.String()
				}
				if pricing.HalfBaseOf(it.Attributes) == want {
					continue
				}
				attrs := maps.Clone(it.Attributes)
				if attrs == nil {
					attrs = map[string]any{}
				}
				if want == "" {
					delete(attrs, pricing.HalfBaseAttr)
				} else {
					attrs[pricing.HalfBaseAttr] = want
				}
				upd := it.Item
				upd.Attributes = attrs
				what := fmt.Sprintf("item %q", it.Name)
				if err := a.tx.UpdateItem(ctx, upd); err != nil {
					return fmt.Errorf("%s: %w", what, err)
				}
				a.halfBaseChanged(what)
			}
		}
	}
	return nil
}

// halfBaseChanged records the change; an item already counted as unchanged
// becomes updated.
func (a *applier) halfBaseChanged(what string) {
	seen := false
	for _, ch := range a.res.Changes {
		seen = seen || strings.HasPrefix(ch, what+": ")
	}
	if !seen {
		c := a.res.Counts["items"]
		if c.Unchanged > 0 {
			c.Unchanged--
		}
		c.Updated++
	}
	a.res.Changes = append(a.res.Changes, what+": half_base")
}

// exportHalfBases names each item's plain pizza (doc items are in the bank's
// order; item = bank item id → name).
func exportHalfBases(bank *menu.Bank, doc *bankdoc.Document, item map[string]string) {
	for i, m := range bank.Menus {
		for j, c := range m.Categories {
			for k, it := range c.Items {
				hb := pricing.HalfBaseOf(it.Attributes)
				if hb == "" {
					continue
				}
				if n, ok := item[hb]; ok {
					doc.Menus[i].Categories[j].Items[k].HalfBase = &n
				}
			}
		}
	}
}
