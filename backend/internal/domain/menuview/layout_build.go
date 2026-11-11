package menuview

import (
	"strings"

	"quiccpos/main/internal/domain/menu"

	"github.com/google/uuid"
)

// BuildLayout is a counter layout in a view's build: every action also says
// what it points at as this view shows it (name, size, price, sold out), and
// a button without a caption gets that name. Prices are never stored in the
// layout itself.
type BuildLayout = LayoutOf[BuildAction, Step]

// BuildAction is a layout action with what the Pi needs to draw and sell it.
type BuildAction struct {
	Action
	// Label is the name of what the button points at: the view's item name,
	// the option, category, deal or function name.
	Label    string `json:"label"`
	SizeName string `json:"size_name,omitempty"`
	// PriceCents: an item's price at its size (the view's price), an option's
	// price on an item without sizes (per-size prices: the group in
	// Build.ModifierGroups).
	PriceCents *int64 `json:"price_cents,omitempty"`
	SoldOut    bool   `json:"sold_out,omitempty"`
	// Missing: not in this build (the view hides it, it's unavailable, or it
	// was deleted since the layout was saved). The terminal greys it out.
	Missing bool `json:"missing,omitempty"`
}

// ResolveLayout puts a counter layout into a compiled build: actions
// resolved against the build (and the bank for what it doesn't show), every
// modifier group a screen uses added to Build.ModifierGroups, and a
// needs-attention line for each reference that no longer resolves. deals
// maps the store's deal ids to their names.
func ResolveLayout(b *Build, bank *menu.Bank, v View, deals map[string]string, l *Layout) {
	items := map[string]*BuildItem{}
	cats := map[string]string{}
	for ci := range b.Categories {
		c := &b.Categories[ci]
		cats[c.Key] = c.Name
		for ii := range c.Items {
			items[c.Items[ii].ID] = &c.Items[ii]
		}
	}
	sizes := map[uuid.UUID]string{}
	for _, z := range b.Sizes {
		sizes[z.ID] = z.Name
	}
	c := compiler{bank: bank, rules: v.Rules, build: b, groupSeen: map[uuid.UUID]bool{}}
	for _, g := range b.ModifierGroups {
		c.groupSeen[g.ID] = true
	}
	missing := map[string]bool{}
	gone := func(code, id, name, msg string) {
		if missing[code+id] {
			return
		}
		missing[code+id] = true
		b.NeedsAttention = append(b.NeedsAttention, NeedsAttention{Code: code, ItemID: id, Name: name, Message: msg})
	}
	act := func(_ string, a Action) (BuildAction, error) {
		out := BuildAction{Action: a}
		switch a.Type {
		case ActItem, ActItemPick:
			if a.ItemID == nil {
				if a.CategoryID != nil {
					out.Label, out.Missing = categoryLabel(bank, cats, *a.CategoryID)
				}
				break
			}
			if a.SizeID != nil {
				out.SizeName = sizes[*a.SizeID]
			}
			bi, ok := items[a.ItemID.String()]
			if !ok {
				out.Missing = true
				name := ""
				if it, _, found := bank.Item(*a.ItemID); found {
					name = it.Name
				}
				out.Label = name
				gone("layout_item_missing", a.ItemID.String(), name, "a counter layout button sells an item this view doesn't show")
				break
			}
			out.Label, out.SoldOut = bi.Name, bi.SoldOut
			price := bi.PriceCents
			if a.SizeID != nil {
				found := false
				for _, z := range bi.Sizes {
					if z.SizeID == *a.SizeID {
						price, found = z.PriceCents, true
					}
				}
				if !found {
					out.Missing = true
					gone("layout_size_missing", a.ItemID.String()+a.SizeID.String(), bi.Name, "a counter layout button sells a size the item no longer comes in")
				}
			}
			out.PriceCents = &price
		case ActCategory:
			if a.CategoryID != nil {
				out.Label, out.Missing = categoryLabel(bank, cats, *a.CategoryID)
			}
		case ActDeal:
			name, ok := deals[a.DealID]
			out.Label, out.Missing = name, !ok
			if !ok {
				gone("layout_deal_missing", a.DealID, "", "a counter layout button opens a deal that no longer exists")
			}
		case ActFunction:
			out.Label = a.Name
		case ActState:
			out.Label = strings.ToUpper(strings.ReplaceAll(a.State, "_", " "))
		case ActOption:
			if a.GroupID == nil || a.OptionID == nil {
				out.Missing = true
				break
			}
			g, ok := bank.Groups[*a.GroupID]
			if !ok {
				out.Missing = true
				gone("layout_option_missing", a.OptionID.String(), "", "a counter layout button picks an option of a deleted group")
				break
			}
			c.groups([]uuid.UUID{g.ID})
			out.Missing = true
			for _, o := range g.Options {
				if o.ID == *a.OptionID {
					p := c.optionPrice(o.PriceDeltaCents)
					out.Label, out.PriceCents, out.SoldOut, out.Missing = o.Name, &p, !o.InStock, false
				}
			}
			if out.Missing {
				gone("layout_option_missing", a.OptionID.String(), g.Name, "a counter layout button picks a deleted option")
			}
		}
		return out, nil
	}
	same := func(_ string, k string) (string, error) { return k, nil }
	step := func(_ string, s Step) (Step, error) { return s, nil }
	out, _ := MapLayout(*l, same, act, step)
	for i := range out.Tabs {
		captions(out.Tabs[i].Buttons)
	}
	for i := range out.Screens {
		captions(out.Screens[i].Buttons)
	}
	for k := range out.Flows {
		if _, ok := items[k]; !ok {
			name := ""
			if id, err := uuid.Parse(k); err == nil {
				if it, _, found := bank.Item(id); found {
					name = it.Name
				}
			}
			gone("layout_item_missing", k, name, "a counter layout flow is for an item this view doesn't show")
		}
	}
	b.Layout = &out
}

// categoryLabel is a category's name as the build shows it (missing when the
// view hides it), else its bank name.
func categoryLabel(bank *menu.Bank, cats map[string]string, id uuid.UUID) (string, bool) {
	if n, ok := cats[id.String()]; ok {
		return n, false
	}
	for _, m := range bank.Menus {
		for _, c := range m.Categories {
			if c.ID == id {
				return c.Name, true
			}
		}
	}
	return "", true
}

func captions(bs []ButtonOf[BuildAction]) {
	for i := range bs {
		if bs[i].Caption == "" {
			bs[i].Caption = bs[i].Action.Label
			if bs[i].Action.SizeName != "" && bs[i].Caption != "" {
				bs[i].Caption = bs[i].Action.SizeName + " " + bs[i].Caption
			}
		}
	}
}
