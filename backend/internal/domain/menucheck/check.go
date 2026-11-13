// Package menucheck finds the mistakes in a location's menu that a manager
// should look at (Store › Menu › Find problems, GET
// /store/v1/me/menu/problems): items the counter can't ring, items that print
// nowhere, prices that look missing and modifier groups that are broken.
//
// It is pure: the bank, the channel views' compiled builds and the counter
// layout in, a list of problems out. Every problem names the bank ids it is
// about, so a page can select the items and fix them in bulk, and the
// console's store check can count them.
package menucheck

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/menuview"

	"github.com/google/uuid"
)

// The four checks, in the order a page shows them.
const (
	CheckPOS     = "pos"     // not on the counter terminal
	CheckPrinter = "printer" // prints nowhere
	CheckPrice   = "price"   // a price looks missing
	CheckMods    = "mods"    // a modifier group is broken
)

// Checks lists the checks in order.
var Checks = []string{CheckPOS, CheckPrinter, CheckPrice, CheckMods}

// Severities: an error is broken for sure; review looks wrong and wants a
// person's eye; info is on purpose (hidden by the store) and never counted.
const (
	SevError  = "error"
	SevReview = "review"
	SevInfo   = "info"
)

// Problem codes.
const (
	CodeNoCounterButton  = "no_counter_button"  // on the counter view, but no button reaches it
	CodeHiddenOnCounter  = "hidden_on_counter"  // the store hides it from the counter (info)
	CodeNoPrinter        = "no_printer"         // no printer of its own or its category's, not receipt-only
	CodeItemZero         = "item_zero"          // a $0 item among priced ones
	CodeSizeZero         = "size_zero"          // one size $0 while another size is priced
	CodeOptionSizeZero   = "option_size_zero"   // an option $0 at a size while priced at another
	CodeOptionZero       = "option_zero"        // a $0 option among priced siblings
	CodeOptionSomeSizes  = "option_some_sizes"  // priced per size, but not for every size its items come in
	CodeOptionFlat       = "option_flat"        // one price for every size in a group priced per size
	CodeScreenWrongGroup = "screen_wrong_group" // a counter screen picks from a group the item doesn't have
	CodeEmptyGroup       = "empty_group"        // a modifier group with no options
	CodeDuplicateOption  = "duplicate_option"   // two options of one group with the same name
	CodeGroupTooSmall    = "group_too_small"    // a group asks for more picks than it has options
)

// View is one channel view and its compiled build.
type View struct {
	View  menuview.View
	Build menuview.Build
}

// Input is everything the checks read.
type Input struct {
	Bank  *menu.Bank
	Views []View
	// Layout is the counter view's layout (nil: the view has none, so every
	// item the view shows is on the counter's category lists).
	Layout *menuview.Layout
}

// Problem is one thing to look at.
type Problem struct {
	Check    string `json:"check"`
	Code     string `json:"code"`
	Severity string `json:"severity"`
	// Message says what is wrong in the manager's words.
	Message    string `json:"message"`
	ItemID     string `json:"item_id,omitempty"`
	ItemName   string `json:"item_name,omitempty"`
	CategoryID string `json:"category_id,omitempty"`
	Category   string `json:"category,omitempty"`
	GroupID    string `json:"group_id,omitempty"`
	GroupName  string `json:"group_name,omitempty"`
	OptionID   string `json:"option_id,omitempty"`
	OptionName string `json:"option_name,omitempty"`
	// Sizes are the sizes the problem is about (names), when it is about some.
	Sizes []string `json:"sizes,omitempty"`
	// Channels: where the item is sold now (web, operator, counter) — the
	// POS check says it so a manager sees what else misses it.
	Channels []string `json:"channels,omitempty"`
}

// Report is the whole result.
type Report struct {
	Version int64 `json:"version"`
	// Counts are the problems per check, info left out.
	Counts   map[string]int `json:"counts"`
	Problems []Problem      `json:"problems"`
}

// Run runs every check.
func Run(in Input) Report {
	r := Report{Counts: map[string]int{}, Problems: []Problem{}}
	if in.Bank == nil {
		return r
	}
	r.Version = in.Bank.Version
	c := newChecker(in)
	c.pos()
	c.printers()
	c.itemPrices()
	c.optionPrices()
	c.mods()
	for _, k := range Checks {
		r.Counts[k] = 0
	}
	for _, p := range c.out {
		if p.Severity != SevInfo {
			r.Counts[p.Check]++
		}
	}
	r.Problems = append(r.Problems, c.out...)
	return r
}

type bankItem struct {
	menu *menu.BankMenu
	cat  *menu.BankCategory
	it   *menu.BankItem
}

type checker struct {
	in       Input
	items    []bankItem
	byID     map[uuid.UUID]bankItem
	sizes    map[uuid.UUID]menu.Size
	channels map[uuid.UUID][]string // item → the view kinds that sell it
	out      []Problem
}

func newChecker(in Input) *checker {
	c := &checker{in: in, byID: map[uuid.UUID]bankItem{}, sizes: map[uuid.UUID]menu.Size{}, channels: map[uuid.UUID][]string{}}
	for _, s := range in.Bank.Sizes {
		c.sizes[s.ID] = s
	}
	for mi := range in.Bank.Menus {
		m := &in.Bank.Menus[mi]
		for ci := range m.Categories {
			cat := &m.Categories[ci]
			for ii := range cat.Items {
				bi := bankItem{menu: m, cat: cat, it: &cat.Items[ii]}
				c.items = append(c.items, bi)
				c.byID[bi.it.ID] = bi
			}
		}
	}
	for _, v := range in.Views {
		if v.View.Kind == menuview.KindVendor {
			continue
		}
		seen := map[uuid.UUID]bool{}
		for _, cat := range v.Build.Categories {
			for _, it := range cat.Items {
				for _, id := range it.Anchor.ItemIDs {
					seen[id] = true
				}
				for _, ch := range it.Choices {
					seen[ch.ItemID] = true
				}
			}
		}
		for id := range seen {
			if !contains(c.channels[id], string(v.View.Kind)) {
				c.channels[id] = append(c.channels[id], string(v.View.Kind))
			}
		}
	}
	for id := range c.channels {
		sort.Strings(c.channels[id])
	}
	return c
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func (c *checker) add(p Problem) { c.out = append(c.out, p) }

func (c *checker) itemProblem(check, code, sev string, bi bankItem, msg string) Problem {
	return Problem{
		Check: check, Code: code, Severity: sev, Message: msg,
		ItemID: bi.it.ID.String(), ItemName: bi.it.Name, CategoryID: bi.cat.ID.String(), Category: bi.cat.Name,
		Channels: c.channels[bi.it.ID],
	}
}

// sold: the item is on at least one channel (an item sold nowhere can't be
// rung wrong, so its prices aren't checked).
func (c *checker) sold(id uuid.UUID) bool { return len(c.channels[id]) > 0 }

// --- the counter ----------------------------------------------------------------

// counterView is the counter view the terminal sells from (the one with a
// layout when there are several).
func (c *checker) counterView() *View {
	var first *View
	for i := range c.in.Views {
		v := &c.in.Views[i]
		if v.View.Kind != menuview.KindCounter {
			continue
		}
		if v.View.HasLayout {
			return v
		}
		if first == nil {
			first = v
		}
	}
	return first
}

func (c *checker) pos() {
	cv := c.counterView()
	if cv == nil {
		return
	}
	reach := map[uuid.UUID]bool{}
	cats := map[uuid.UUID]bool{}
	if l := c.in.Layout; l != nil {
		for _, t := range l.Tabs {
			for _, b := range t.Buttons {
				markReach(b.Action, reach, cats)
			}
		}
		for _, s := range l.Screens {
			for _, b := range s.Buttons {
				if b.Action.Type == menuview.ActItemPick && b.Action.ItemID != nil {
					reach[*b.Action.ItemID] = true
				}
			}
		}
	}
	hiddenCat := map[uuid.UUID]bool{}
	for _, id := range cv.View.Rules.HiddenCategories {
		hiddenCat[id] = true
	}
	menus := map[uuid.UUID]bool{}
	for _, id := range cv.View.Rules.Menus {
		menus[id] = true
	}
	for _, bi := range c.items {
		if bi.it.QuickAdd {
			continue
		}
		onCounter := contains(c.channels[bi.it.ID], string(menuview.KindCounter))
		if !onCounter {
			why := "the Counter view hides it"
			switch {
			case len(menus) > 0 && !menus[bi.menu.ID]:
				why = fmt.Sprintf("its menu “%s” isn’t on the counter", bi.menu.Name)
			case hiddenCat[bi.cat.ID]:
				why = fmt.Sprintf("its category “%s” is hidden on the counter", bi.cat.Name)
			case !bi.menu.Available:
				why = fmt.Sprintf("its menu “%s” is turned off", bi.menu.Name)
			case !bi.cat.Available:
				why = fmt.Sprintf("its category “%s” is turned off", bi.cat.Name)
			}
			msg := "Not on the counter: " + why + "."
			if !c.sold(bi.it.ID) {
				msg += " It isn’t on the website or the call center either."
			}
			c.add(c.itemProblem(CheckPOS, CodeHiddenOnCounter, SevInfo, bi, msg))
			continue
		}
		if c.in.Layout != nil && !reach[bi.it.ID] && !cats[bi.cat.ID] {
			c.add(c.itemProblem(CheckPOS, CodeNoCounterButton, SevError, bi,
				"On the counter’s menu, but no button on the counter screens rings it up."))
		}
	}
}

func markReach(a menuview.Action, reach, cats map[uuid.UUID]bool) {
	switch {
	case (a.Type == menuview.ActItem || a.Type == menuview.ActItemPick) && a.ItemID != nil:
		reach[*a.ItemID] = true
	case (a.Type == menuview.ActCategory || a.Type == menuview.ActItem) && a.CategoryID != nil:
		cats[*a.CategoryID] = true
	}
}

// --- printers -------------------------------------------------------------------

func (c *checker) printers() {
	for _, bi := range c.items {
		it, cat := bi.it, bi.cat
		receiptOnly := it.NoKitchenTicket || (it.PrinterID == nil && cat.NoKitchenTicket)
		if it.PrinterID != nil || cat.DefaultPrinterID != nil || receiptOnly {
			continue
		}
		msg := "No kitchen printer: it prints on every station."
		if it.QuickAdd {
			msg = "Quick add has no kitchen printer: the terminal’s typed items print on every station."
		}
		c.add(c.itemProblem(CheckPrinter, CodeNoPrinter, SevError, bi, msg))
	}
}

// --- prices ---------------------------------------------------------------------

func optionPriced(o menu.BankOption) bool {
	if o.PriceDeltaCents > 0 {
		return true
	}
	for _, p := range o.Prices {
		if p.PriceCents > 0 {
			return true
		}
	}
	return false
}

// pricedByChoice: the item's price comes from a required choice (a $0 dish
// whose first step picks the priced plate).
func (c *checker) pricedByChoice(it *menu.BankItem) bool {
	for _, gid := range it.GroupIDs {
		g, ok := c.in.Bank.Groups[gid]
		if !ok || !(g.Required || g.MinSelect > 0) {
			continue
		}
		for _, o := range g.Options {
			if optionPriced(o) {
				return true
			}
		}
	}
	return false
}

func openPrice(it *menu.BankItem) bool {
	v, _ := it.Attributes["open_price"].(bool)
	return v
}

func (c *checker) receiptOnly(bi bankItem) bool {
	return bi.it.NoKitchenTicket || (bi.it.PrinterID == nil && bi.cat.NoKitchenTicket)
}

func (c *checker) itemPrices() {
	for _, bi := range c.items {
		it := bi.it
		if it.QuickAdd || openPrice(it) || !c.sold(it.ID) || c.pricedByChoice(it) {
			continue
		}
		if len(it.Sizes) > 0 {
			var zero, priced []string
			for _, s := range it.Sizes {
				if s.PriceCents == 0 {
					zero = append(zero, c.sizeName(s.SizeID))
				} else {
					priced = append(priced, c.sizeName(s.SizeID))
				}
			}
			if len(zero) > 0 && len(priced) > 0 {
				p := c.itemProblem(CheckPrice, CodeSizeZero, SevReview, bi,
					fmt.Sprintf("%s %s $0.00 while %s %s a price.", strings.Join(zero, ", "), isAre(len(zero)), strings.Join(priced, ", "), hasHave(len(priced))))
				p.Sizes = zero
				c.add(p)
				continue
			}
			if len(priced) > 0 {
				continue
			}
		} else if it.PriceCents > 0 {
			continue
		}
		// A $0 item: worth a look when most of its category costs money
		// (free extras in a category of free extras are fine; receipt-only
		// items are drinks / functions the store set up on purpose).
		if c.receiptOnly(bi) {
			continue
		}
		var others, pricedOthers int
		for _, o := range bi.cat.Items {
			if o.ID == it.ID || o.QuickAdd {
				continue
			}
			others++
			if itemPriced(&o) {
				pricedOthers++
			}
		}
		if others >= 2 && pricedOthers*2 >= others {
			c.add(c.itemProblem(CheckPrice, CodeItemZero, SevReview, bi,
				fmt.Sprintf("$0.00, while %d of the %d other items in %s cost money.", pricedOthers, others, bi.cat.Name)))
		}
	}
}

func itemPriced(it *menu.BankItem) bool {
	if it.PriceCents > 0 {
		return true
	}
	for _, s := range it.Sizes {
		if s.PriceCents > 0 {
			return true
		}
	}
	return false
}

func (c *checker) sizeName(id uuid.UUID) string {
	if s, ok := c.sizes[id]; ok {
		return s.Name
	}
	return "a size"
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func hasHave(n int) string {
	if n == 1 {
		return "has"
	}
	return "have"
}

// Option names that are free on purpose (removals, how it's cooked, on the side).
var freeName = regexp.MustCompile(`(?i)^\s*(no|lite|light|less|well|on side|side|plain|regular|extra light|xtra light|easy|cut|toast|hot|mild|medium|spicy)\b`)

// An add-on: "Add Egg", "Extra Egg", "Xtra Cheese".
var addName = regexp.MustCompile(`(?i)^\s*(add|extra|xtra|x-tra)\b\.?\s*`)

func dollars(cents int64) string { return fmt.Sprintf("$%d.%02d", cents/100, cents%100) }

func (c *checker) groupSizes() map[uuid.UUID][]uuid.UUID {
	set := map[uuid.UUID]map[uuid.UUID]bool{}
	for _, bi := range c.items {
		if !c.sold(bi.it.ID) && !bi.it.QuickAdd {
			continue
		}
		for _, gid := range bi.it.GroupIDs {
			if set[gid] == nil {
				set[gid] = map[uuid.UUID]bool{}
			}
			for _, s := range bi.it.Sizes {
				set[gid][s.SizeID] = true
			}
		}
	}
	out := map[uuid.UUID][]uuid.UUID{}
	for gid, m := range set {
		for _, s := range c.in.Bank.Sizes { // store order
			if m[s.ID] {
				out[gid] = append(out[gid], s.ID)
			}
		}
	}
	return out
}

func (c *checker) groupsInOrder() []menu.BankGroup {
	gs := make([]menu.BankGroup, 0, len(c.in.Bank.Groups))
	for _, g := range c.in.Bank.Groups {
		gs = append(gs, g)
	}
	sort.Slice(gs, func(i, j int) bool {
		if gs[i].Name != gs[j].Name {
			return strings.ToLower(gs[i].Name) < strings.ToLower(gs[j].Name)
		}
		return gs[i].ID.String() < gs[j].ID.String()
	})
	return gs
}

func optionProblem(code, sev string, g menu.BankGroup, o menu.BankOption, msg string) Problem {
	return Problem{Check: CheckPrice, Code: code, Severity: sev, Message: msg,
		GroupID: g.ID.String(), GroupName: g.Name, OptionID: o.ID.String(), OptionName: o.Name}
}

func (c *checker) optionPrices() {
	used := c.groupSizes()
	for _, g := range c.groupsInOrder() {
		if len(used[g.ID]) == 0 && !c.groupOnSoldItem(g.ID) {
			continue
		}
		perSize := 0
		for _, o := range g.Options {
			if len(o.Prices) > 0 {
				perSize++
			}
		}
		for _, o := range g.Options {
			free := freeName.MatchString(o.Name)
			prices := map[uuid.UUID]int64{}
			for _, p := range o.Prices {
				prices[p.SizeID] = p.PriceCents
			}
			// (1) $0 at some sizes, priced at others.
			if len(prices) > 0 && !free {
				var zero []string
				var pricedAt string
				for _, sid := range c.sizeOrder(prices) {
					if prices[sid] == 0 {
						zero = append(zero, c.sizeName(sid))
					} else if pricedAt == "" {
						pricedAt = fmt.Sprintf("%s at %s", dollars(prices[sid]), c.sizeName(sid))
					}
				}
				if len(zero) > 0 && pricedAt != "" {
					p := optionProblem(CodeOptionSizeZero, SevReview, g, o,
						fmt.Sprintf("$0.00 at %s, but %s.", strings.Join(zero, ", "), pricedAt))
					p.Sizes = zero
					c.add(p)
				}
			}
			// (2) priced per size, but not for every size its items come in:
			// those sizes fall back to its one price.
			if len(prices) > 0 && optionPriced(o) {
				var missing []string
				for _, sid := range used[g.ID] {
					if _, ok := prices[sid]; !ok {
						missing = append(missing, c.sizeName(sid))
					}
				}
				if len(missing) > 0 {
					p := optionProblem(CodeOptionSomeSizes, SevReview, g, o,
						fmt.Sprintf("No price for %s: %s %s.", strings.Join(missing, ", "), fallsBack(len(missing)), dollars(o.PriceDeltaCents)))
					p.Sizes = missing
					c.add(p)
				}
			}
			// (3) one price for every size in a group whose options are priced per size.
			if len(prices) == 0 && o.PriceDeltaCents > 0 && len(used[g.ID]) > 1 && perSize*2 >= len(g.Options) {
				c.add(optionProblem(CodeOptionFlat, SevReview, g, o,
					fmt.Sprintf("%s at every size, while most of %s is priced per size.", dollars(o.PriceDeltaCents), g.Name)))
			}
			// (4) a $0 add-on that looks like it should cost.
			if !optionPriced(o) && !free && !o.ShowOnDriverTicket {
				if why := c.zeroOptionReason(g, o); why != "" {
					c.add(optionProblem(CodeOptionZero, SevReview, g, o, "$0.00, "+why+"."))
				}
			}
		}
	}
}

func fallsBack(n int) string {
	if n == 1 {
		return "that size charges its one price,"
	}
	return "those sizes charge its one price,"
}

// groupOnSoldItem: a group attached to an item some channel sells.
func (c *checker) groupOnSoldItem(gid uuid.UUID) bool {
	for _, bi := range c.items {
		if !c.sold(bi.it.ID) {
			continue
		}
		for _, id := range bi.it.GroupIDs {
			if id == gid {
				return true
			}
		}
	}
	return false
}

func (c *checker) sizeOrder(m map[uuid.UUID]int64) []uuid.UUID {
	var out []uuid.UUID
	for _, s := range c.in.Bank.Sizes {
		if _, ok := m[s.ID]; ok {
			out = append(out, s.ID)
		}
	}
	return out
}

func normName(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// core: an add-on's name without "Add" / "Extra" ("Add Egg" = "Extra Egg").
func core(s string) string {
	n := normName(addName.ReplaceAllString(s, ""))
	n = strings.ReplaceAll(n, "chz", "cheese")
	return strings.Trim(n, " .")
}

// zeroOptionReason says why a $0 option looks like a missing price, or "".
func (c *checker) zeroOptionReason(g menu.BankGroup, o menu.BankOption) string {
	var others, pricedOthers, adds, pricedAdds int
	// An add-on with a name of its own ("Add Egg"; a bare "Extra" is a note).
	isAdd := addName.MatchString(o.Name) && core(o.Name) != ""
	for _, x := range g.Options {
		if x.ID == o.ID || freeName.MatchString(x.Name) {
			continue
		}
		others++
		px := optionPriced(x)
		if px {
			pricedOthers++
		}
		if addName.MatchString(x.Name) {
			adds++
			if px {
				pricedAdds++
			}
			// "Add Egg" free while "Extra Egg" costs.
			if isAdd && px && core(x.Name) == core(o.Name) {
				return fmt.Sprintf("while “%s” is %s", x.Name, dollars(c.anyPrice(x)))
			}
		}
	}
	if others >= 3 && pricedOthers*10 >= others*9 {
		return fmt.Sprintf("while %d of the other %d choices in %s cost money", pricedOthers, others, g.Name)
	}
	if isAdd && adds >= 3 && pricedAdds*10 >= adds*6 {
		return fmt.Sprintf("while %d of the other %d add-ons in %s cost money", pricedAdds, adds, g.Name)
	}
	return ""
}

// anyPrice: the option's one price, else its first per-size price (store order).
func (c *checker) anyPrice(o menu.BankOption) int64 {
	if o.PriceDeltaCents > 0 {
		return o.PriceDeltaCents
	}
	for _, s := range c.in.Bank.Sizes {
		for _, p := range o.Prices {
			if p.SizeID == s.ID && p.PriceCents > 0 {
				return p.PriceCents
			}
		}
	}
	return 0
}

// --- modifier groups ------------------------------------------------------------

func (c *checker) mods() {
	// A counter screen that picks from a group the item doesn't have: the
	// engine refuses those picks, so none of the screen's buttons work on it.
	if l := c.in.Layout; l != nil {
		screens := map[string]menuview.ScreenOf[menuview.Action]{}
		for _, s := range l.Screens {
			screens[normName(s.Name)] = s
		}
		ids := make([]string, 0, len(l.Flows))
		for id := range l.Flows {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		seen := map[string]bool{}
		for _, id := range ids {
			uid, err := uuid.Parse(id)
			if err != nil {
				continue
			}
			bi, ok := c.byID[uid]
			if !ok {
				continue
			}
			has := map[uuid.UUID]bool{}
			for _, g := range bi.it.GroupIDs {
				has[g] = true
			}
			for _, st := range l.Flows[id] {
				s, ok := screens[normName(st.Screen)]
				if !ok {
					continue
				}
				for _, b := range s.Buttons {
					a := b.Action
					if a.Type != menuview.ActOption || a.GroupID == nil || has[*a.GroupID] {
						continue
					}
					size := ""
					if st.SizeID != nil {
						size = c.sizeName(*st.SizeID)
					}
					key := id + "|" + size + "|" + s.Name + "|" + a.GroupID.String()
					if seen[key] {
						continue
					}
					seen[key] = true
					gname := "a group that no longer exists"
					if g, ok := c.in.Bank.Groups[*a.GroupID]; ok {
						gname = "“" + g.Name + "”"
					}
					at := ""
					if size != "" {
						at = " at " + size
					}
					p := c.itemProblem(CheckMods, CodeScreenWrongGroup, SevError, bi,
						fmt.Sprintf("Its counter screen “%s”%s picks from %s, which the item doesn’t have: those buttons don’t work on it.", s.Name, at, gname))
					p.GroupID = a.GroupID.String()
					if g, ok := c.in.Bank.Groups[*a.GroupID]; ok {
						p.GroupName = g.Name
					}
					if size != "" {
						p.Sizes = []string{size}
					}
					c.add(p)
				}
			}
		}
	}
	uses := map[uuid.UUID]int{}
	for _, bi := range c.items {
		for _, g := range bi.it.GroupIDs {
			uses[g]++
		}
	}
	for _, g := range c.groupsInOrder() {
		gp := func(code, sev, msg string) Problem {
			return Problem{Check: CheckMods, Code: code, Severity: sev, Message: msg, GroupID: g.ID.String(), GroupName: g.Name}
		}
		if len(g.Options) == 0 {
			sev, msg := SevReview, "No options, and no item uses it."
			if uses[g.ID] > 0 {
				sev, msg = SevError, fmt.Sprintf("No options, but %d item%s use%s it.", uses[g.ID], plural(uses[g.ID]), verbS(uses[g.ID]))
			}
			c.add(gp(CodeEmptyGroup, sev, msg))
			continue
		}
		need := g.MinSelect
		if g.Required && need < 1 {
			need = 1
		}
		if need > len(g.Options) && uses[g.ID] > 0 {
			c.add(gp(CodeGroupTooSmall, SevError,
				fmt.Sprintf("Asks for %d pick%s but has only %d option%s: its items can’t be ordered.", need, plural(need), len(g.Options), plural(len(g.Options)))))
		}
		byName := map[string][]menu.BankOption{}
		var order []string
		for _, o := range g.Options {
			k := normName(o.Name)
			if _, ok := byName[k]; !ok {
				order = append(order, k)
			}
			byName[k] = append(byName[k], o)
		}
		for _, k := range order {
			os := byName[k]
			if len(os) < 2 {
				continue
			}
			p := gp(CodeDuplicateOption, SevReview,
				fmt.Sprintf("“%s” is in it %d times.", os[0].Name, len(os)))
			p.OptionID, p.OptionName = os[1].ID.String(), os[1].Name
			c.add(p)
		}
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func verbS(n int) string {
	if n == 1 {
		return "s"
	}
	return ""
}
