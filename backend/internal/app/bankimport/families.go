package bankimport

import (
	"context"
	"fmt"
	"strings"

	"quiccpos/main/internal/domain/bankdoc"
	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/pricing"

	"github.com/google/uuid"
)

// Families (00045): the document's families match the store's by kind +
// name; items and options name theirs (absent keeps, "" = none); coupon parts
// name item families and sizes. prune deletes families only when the
// document has a families list. Runs after the tree (so new items and
// options exist) and before the deals.

func famKey(kind, name string) string { return kind + "|" + bankdoc.Norm(name) }

func (a *applier) applyFamilies(ctx context.Context) error {
	have := map[string]menu.Family{}
	for _, f := range a.bank.Families {
		if _, dup := have[famKey(f.Kind, f.Name)]; !dup {
			have[famKey(f.Kind, f.Name)] = f
		}
	}
	a.fams = map[string]uuid.UUID{}
	kept := map[uuid.UUID]bool{}
	for _, df := range a.doc.Families {
		f := menu.Family{Kind: df.Kind, Name: df.Name, Class: df.Class}
		_ = f.Normalize() // checkShape checked it
		key := famKey(f.Kind, f.Name)
		what := fmt.Sprintf("%s family %q", f.Kind, f.Name)
		c, ok := have[key]
		if !ok {
			id, err := a.tx.CreateFamily(ctx, f)
			if err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
			a.fams[key] = id
			a.change("families", what, true, nil)
			continue
		}
		kept[c.ID] = true
		a.fams[key] = c.ID
		var fl []string
		fl = diff(fl, "name", c.Name, f.Name)
		fl = diff(fl, "class", c.Class, f.Class)
		if len(fl) > 0 {
			f.ID = c.ID
			if err := a.tx.UpdateFamily(ctx, f); err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
		}
		a.change("families", what, false, fl)
	}
	// prune deletes the families a document that lists families doesn't
	// name (a document without the key leaves them all alone).
	prune := a.o.Prune && a.doc.Families != nil
	if !prune { // the store's other families stay usable by name
		for key, f := range have {
			if _, ok := a.fams[key]; !ok {
				a.fams[key] = f.ID
			}
		}
	}
	target := func(path, kind string, name *string) (*uuid.UUID, error) {
		if strings.TrimSpace(*name) == "" {
			return nil, nil
		}
		id, ok := a.fams[famKey(kind, *name)]
		if !ok {
			return nil, bad(path, "unknown %s family %q", kind, *name)
		}
		return &id, nil
	}
	type move struct {
		option bool
		family *uuid.UUID
	}
	moves := map[string][]uuid.UUID{}
	moveOf := map[string]move{}
	add := func(option bool, id uuid.UUID, cur, next *uuid.UUID, what string) {
		if idStr(cur) == idStr(next) {
			return
		}
		k := fmt.Sprintf("%v|%s", option, idStr(next))
		moves[k] = append(moves[k], id)
		moveOf[k] = move{option: option, family: next}
		a.res.Counts["family_links"].Updated++
		a.res.Changes = append(a.res.Changes, what+": family")
	}
	for i, m := range a.doc.Menus {
		for j, c := range m.Categories {
			for k, di := range c.Items {
				if di.Family == nil {
					continue
				}
				path := fmt.Sprintf("menus[%d].categories[%d].items[%d].family", i, j, k)
				next, err := target(path, menu.FamilyItem, di.Family)
				if err != nil {
					return err
				}
				id, ok := a.items[bankdoc.Norm(di.Name)]
				if !ok {
					continue
				}
				bi, _, ok := a.bank.Item(id)
				if !ok {
					continue
				}
				add(false, id, bi.FamilyID, next, fmt.Sprintf("item %q", strings.TrimSpace(di.Name)))
			}
		}
	}
	for i, dg := range a.doc.Groups {
		gid, ok := a.groups[bankdoc.Norm(dg.Name)]
		if !ok {
			continue
		}
		bg, ok := a.bank.Groups[gid]
		if !ok {
			continue
		}
		for j, do := range dg.Options {
			if do.Family == nil {
				continue
			}
			path := fmt.Sprintf("groups[%d].options[%d].family", i, j)
			next, err := target(path, menu.FamilyMod, do.Family)
			if err != nil {
				return err
			}
			for _, bo := range bg.Options {
				if bankdoc.Norm(bo.Name) == bankdoc.Norm(do.Name) {
					add(true, bo.ID, bo.FamilyID, next, fmt.Sprintf("option %q of %q", strings.TrimSpace(do.Name), strings.TrimSpace(dg.Name)))
					break
				}
			}
		}
	}
	for k, ids := range moves {
		mv := moveOf[k]
		if err := a.tx.SetFamily(ctx, mv.option, ids, mv.family); err != nil {
			return err
		}
	}
	if prune {
		for _, f := range a.bank.Families {
			if !kept[f.ID] {
				if err := a.tx.DeleteFamily(ctx, f.ID); err != nil {
					return err
				}
				a.deleted("families", fmt.Sprintf("%s family %q", f.Kind, f.Name))
			}
		}
	}
	return nil
}

// couponPart turns a coupon part's family and size names into ids.
func (a *applier) couponPart(path string, p *pricing.Part) error {
	for i, n := range p.Families {
		id, ok := a.fams[famKey(menu.FamilyItem, n)]
		if !ok {
			return bad(fmt.Sprintf("%s.families[%d]", path, i), "unknown item family %q", n)
		}
		p.Families[i] = id.String()
	}
	for i, n := range p.UpchargeOver {
		id, ok := a.fams[famKey(menu.FamilyItem, n)]
		if !ok {
			return bad(fmt.Sprintf("%s.upcharge_over[%d]", path, i), "unknown item family %q", n)
		}
		p.UpchargeOver[i] = id.String()
	}
	for i, n := range p.Sizes {
		id, err := a.sizeID(fmt.Sprintf("%s.sizes[%d]", path, i), n)
		if err != nil {
			return err
		}
		p.Sizes[i] = id.String()
	}
	return nil
}

// exportFamilies writes the bank's families and every item's / option's
// family (by name) into the document; the coupons' parts are named by
// exportDealNames.
func exportFamilies(bank *menu.Bank, doc *bankdoc.Document) map[string]string {
	name := map[string]string{}
	for _, f := range bank.Families {
		name[f.ID.String()] = f.Name
		df := bankdoc.Family{Name: f.Name, Kind: f.Kind}
		if f.Kind == menu.FamilyMod {
			df.Class = f.Class
		}
		doc.Families = append(doc.Families, df)
	}
	itemFam := map[string]*uuid.UUID{}
	for _, m := range bank.Menus {
		for _, c := range m.Categories {
			for _, it := range c.Items {
				itemFam[bankdoc.Norm(it.Name)] = it.FamilyID
			}
		}
	}
	for i := range doc.Menus {
		for j := range doc.Menus[i].Categories {
			for k := range doc.Menus[i].Categories[j].Items {
				di := &doc.Menus[i].Categories[j].Items[k]
				if f := itemFam[bankdoc.Norm(di.Name)]; f != nil {
					n := name[f.String()]
					di.Family = &n
				}
			}
		}
	}
	for i := range doc.Groups {
		dg := &doc.Groups[i]
		for _, bg := range bank.Groups {
			if bankdoc.Norm(bg.Name) != bankdoc.Norm(dg.Name) {
				continue
			}
			for j := range dg.Options {
				for _, bo := range bg.Options {
					if bo.FamilyID != nil && bankdoc.Norm(bo.Name) == bankdoc.Norm(dg.Options[j].Name) {
						n := name[bo.FamilyID.String()]
						dg.Options[j].Family = &n
						break
					}
				}
			}
			break
		}
	}
	return name
}
