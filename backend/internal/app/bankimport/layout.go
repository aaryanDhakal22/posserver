package bankimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"quiccpos/main/internal/domain/bankdoc"
	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/menuview"

	"github.com/google/uuid"
)

// --- counter layouts (TERMINAL-PLAN.md 0a) ---

// applyLayouts writes each counter view's layout the document carries (names
// → ids), after the views and deals it may point at. A view without a
// layout in the document keeps its own.
func (a *applier) applyLayouts(ctx context.Context) error {
	var views []menuview.View
	for i, dv := range a.doc.Views {
		if dv.Layout == nil {
			continue
		}
		path := fmt.Sprintf("views[%d].layout", i)
		if views == nil {
			var err error
			if views, err = a.tx.Views(ctx); err != nil {
				return err
			}
		}
		var v *menuview.View
		for j := range views {
			if string(views[j].Kind) == dv.Kind && bankdoc.Norm(views[j].Name) == bankdoc.Norm(dv.Name) {
				v = &views[j]
				break
			}
		}
		if v == nil {
			return fmt.Errorf("%s: view %s %q not found after apply", path, dv.Kind, dv.Name)
		}
		deals, err := a.tx.Deals(ctx)
		if err != nil {
			return err
		}
		dealIDs, dealNames := map[string]string{}, map[string]string{}
		for _, d := range deals {
			if _, dup := dealIDs[bankdoc.Norm(d.Name)]; !dup {
				dealIDs[bankdoc.Norm(d.Name)] = d.ID
			}
			dealNames[d.ID] = d.Name
		}
		l, err := a.layoutIDs(path, *dv.Layout, dealIDs)
		if err != nil {
			return err
		}
		menuview.NormalizeLayout(&l)
		if ps := menuview.ValidateLayout(&l, menuview.LayoutRefs{Bank: a.bank, Deals: dealNames}); len(ps) > 0 {
			return bad(path+"."+ps[0].Path, "%s", ps[0].Message)
		}
		what := fmt.Sprintf("layout of view %s %q", v.Kind, v.Name)
		cur, err := a.tx.Layout(ctx, v.ID)
		if err != nil {
			return err
		}
		if cur != nil && sameLayout(*cur, l) {
			a.change("layouts", what, false, nil)
			continue
		}
		if err := a.tx.SetLayout(ctx, v.ID, &l); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		a.change("layouts", what, cur == nil, []string{"layout"})
	}
	return nil
}

func sameLayout(a, b menuview.Layout) bool {
	ra, err1 := json.Marshal(a)
	rb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(ra, rb)
}

// layoutIDs resolves a document layout's names against the bank as it now is.
func (a *applier) layoutIDs(path string, dl bankdoc.Layout, deals map[string]string) (menuview.Layout, error) {
	opt := func(p, group, option string) (*uuid.UUID, *uuid.UUID, error) {
		gid, ok := a.groups[bankdoc.Norm(group)]
		if !ok {
			return nil, nil, bad(p+".group", "unknown modifier group %q", group)
		}
		for _, o := range a.bank.Groups[gid].Options {
			if bankdoc.Norm(o.Name) == bankdoc.Norm(option) {
				oid := o.ID
				return &gid, &oid, nil
			}
		}
		return nil, nil, bad(p+".option", "group %q has no option %q", group, option)
	}
	ref := func(p string, f func(string, string) (uuid.UUID, error), name string) (*uuid.UUID, error) {
		if strings.TrimSpace(name) == "" {
			return nil, nil
		}
		id, err := f(p, name)
		if err != nil {
			return nil, err
		}
		return &id, nil
	}
	act := func(p string, da bankdoc.LayoutAction) (menuview.Action, error) {
		x := menuview.Action{Type: da.Type, Name: da.Name, State: da.State}
		var err error
		if x.ItemID, err = ref(p+".item", a.itemID, da.Item); err != nil {
			return x, err
		}
		if x.SizeID, err = ref(p+".size", a.sizeID, da.Size); err != nil {
			return x, err
		}
		if x.CategoryID, err = ref(p+".category", a.catID, da.Category); err != nil {
			return x, err
		}
		if strings.TrimSpace(da.Deal) != "" {
			id, ok := deals[bankdoc.Norm(da.Deal)]
			if !ok {
				return x, bad(p+".deal", "unknown deal %q", da.Deal)
			}
			x.DealID = id
		}
		if da.Group != "" || da.Option != "" {
			if x.GroupID, x.OptionID, err = opt(p, da.Group, da.Option); err != nil {
				return x, err
			}
		}
		return x, nil
	}
	key := func(p, name string) (string, error) {
		id, err := a.itemID(p, name)
		return id.String(), err
	}
	step := func(p string, ds bankdoc.LayoutStep) (menuview.Step, error) {
		s := menuview.Step{Screen: ds.Screen, RequiredCount: ds.RequiredCount, Forced: ds.Forced, Prompt: ds.Prompt}
		var err error
		s.SizeID, err = ref(p+".size", a.sizeID, ds.Size)
		return s, err
	}
	l, err := menuview.MapLayout(dl, key, act, step)
	if err != nil {
		var e *BadDocError
		if errors.As(err, &e) {
			return l, &BadDocError{Path: path + "." + e.Path, Msg: e.Msg}
		}
		return l, bad(path, "%v", err)
	}
	return l, nil
}

// exportLayout writes a counter layout by names (an id that no longer
// resolves is written as is, like the rest of the export).
func exportLayout(l menuview.Layout, bank *menu.Bank, item, cat map[string]string, size map[uuid.UUID]string,
	group map[uuid.UUID]string, deals map[string]string) bankdoc.Layout {
	option := map[uuid.UUID]string{}
	for _, g := range bank.Groups {
		for _, o := range g.Options {
			option[o.ID] = o.Name
		}
	}
	sizeName := func(id *uuid.UUID) string {
		if id == nil {
			return ""
		}
		if n, ok := size[*id]; ok {
			return n
		}
		return id.String()
	}
	idName := func(m map[string]string, id *uuid.UUID) string {
		if id == nil {
			return ""
		}
		return nameOr(m, id.String())
	}
	uName := func(m map[uuid.UUID]string, id *uuid.UUID) string {
		if id == nil {
			return ""
		}
		if n, ok := m[*id]; ok {
			return n
		}
		return id.String()
	}
	act := func(_ string, x menuview.Action) (bankdoc.LayoutAction, error) {
		da := bankdoc.LayoutAction{Type: x.Type, Name: x.Name, State: x.State, Item: idName(item, x.ItemID),
			Size: sizeName(x.SizeID), Category: idName(cat, x.CategoryID), Group: uName(group, x.GroupID), Option: uName(option, x.OptionID)}
		if x.DealID != "" {
			da.Deal = nameOr(deals, x.DealID)
		}
		return da, nil
	}
	key := func(_ string, k string) (string, error) { return nameOr(item, k), nil }
	step := func(_ string, s menuview.Step) (bankdoc.LayoutStep, error) {
		return bankdoc.LayoutStep{Screen: s.Screen, Size: sizeName(s.SizeID), RequiredCount: s.RequiredCount, Forced: s.Forced,
			Prompt: s.Prompt}, nil
	}
	out, _ := menuview.MapLayout(l, key, act, step)
	return out
}
