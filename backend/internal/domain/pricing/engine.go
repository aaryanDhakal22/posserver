package pricing

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"quiccpos/main/internal/domain/menuview"

	"github.com/google/uuid"
)

// Pct is amount × bps / 10000, rounded half up (half away from zero for a
// negative amount). Every percentage in a quote goes through it.
func Pct(amount int64, bps int) int64 {
	if amount < 0 {
		return -Pct(-amount, bps)
	}
	return (amount*int64(bps) + 5000) / 10000
}

// Money formats cents as "$12.50" ("−$1.00" below zero).
func Money(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "−", -c
	}
	return fmt.Sprintf("%s$%d.%02d", sign, c/100, c%100)
}

var weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func hhmm(s string) (int, bool) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// Live reports whether a deal runs at `at` for an order type ("" = any).
func Live(d Deal, tz string, at time.Time, orderType string) bool {
	if !d.Active {
		return false
	}
	if d.Fulfil != "" && d.Fulfil != "both" && orderType != "" && d.Fulfil != orderType {
		return false
	}
	s := d.Schedule
	if s == nil {
		return true
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	local := at.In(loc)
	if len(s.Days) > 0 {
		day := weekdays[local.Weekday()]
		ok := false
		for _, d := range s.Days {
			if d == day {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	now := local.Hour()*60 + local.Minute()
	start, hasStart := hhmm(s.Start)
	end, hasEnd := hhmm(s.End)
	switch {
	case hasStart && hasEnd && end <= start: // overnight, e.g. 22:00–02:00
		return now >= start || now < end
	case hasStart && now < start:
		return false
	case hasEnd && now >= end:
		return false
	}
	return true
}

// unit is one eligible-for-deals unit of a cart item line.
type unit struct {
	price    int64
	item     string // bank item id ("" = not deal-eligible, e.g. an added deal item)
	category string
	size     string
}

func matches(el Eligible, u unit) bool {
	if u.item == "" {
		return false
	}
	if !contains(el.Items, u.item) && !(u.category != "" && contains(el.Categories, u.category)) {
		return false
	}
	if len(el.Sizes) > 0 {
		return u.size != "" && contains(el.Sizes, u.size)
	}
	return true
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// priced is one configured item.
type priced struct {
	name     string
	mods     []string
	base     int64
	extras   int64 // charged extras (after any free options)
	unit     int64
	bankItem string // deal eligibility ("" = none)
	rateItem string // whose tax rate applies
	size     string
	issues   []Issue
}

type engine struct {
	ctx    Context
	items  map[string]menuview.BuildItem
	groups map[string]menuview.BuildGroup
}

func newEngine(ctx Context) *engine {
	e := &engine{ctx: ctx, items: map[string]menuview.BuildItem{}, groups: map[string]menuview.BuildGroup{}}
	for _, c := range ctx.Menu.Categories {
		for _, it := range c.Items {
			if _, dup := e.items[it.ID]; !dup {
				e.items[it.ID] = it
			}
		}
	}
	for _, g := range ctx.Menu.ModifierGroups {
		e.groups[g.ID.String()] = g
	}
	return e
}

func (e *engine) category(bankItem string) string {
	return e.ctx.Items[bankItem].CategoryID
}

// rate is a bank item's tax rate (its own, else the store's).
func (e *engine) rate(bankItem string) int {
	if r := e.ctx.Items[bankItem].TaxBps; r > 0 {
		return r
	}
	return e.ctx.Settings.TaxBps
}

// price prices one configured item; the priciest `free` charged options are
// included (a bundle slot's).
func (e *engine) price(sel Selection, free int) priced {
	it, ok := e.items[sel.ItemID]
	if !ok {
		return priced{name: "Unknown item", mods: []string{}, issues: []Issue{{Code: IssueUnknownItem, Message: "An item in your order is no longer on the menu."}}}
	}
	p := priced{name: it.Name, mods: []string{}}
	sizes, base, soldOut := it.Sizes, it.PriceCents, it.SoldOut
	switch {
	case len(it.Choices) > 0: // a combined item: the customer picks one
		var ch *menuview.BuildChoice
		for i := range it.Choices {
			if it.Choices[i].ItemID.String() == sel.ChoiceID {
				ch = &it.Choices[i]
			}
		}
		if ch == nil {
			p.issues = append(p.issues, Issue{Code: IssueOptionRules, Message: it.Name + ": choose one."})
			return p
		}
		p.mods = append(p.mods, ch.Name)
		sizes, base, soldOut = ch.Sizes, ch.PriceCents, ch.SoldOut
		p.bankItem, p.rateItem = ch.ItemID.String(), ch.ItemID.String()
	case len(it.Anchor.ItemIDs) > 0 && it.Anchor.ItemIDs[0].String() == it.ID:
		p.bankItem, p.rateItem = it.ID, it.ID // a bank item (maybe renamed / repriced)
	case len(it.Anchor.ItemIDs) > 0:
		p.rateItem = it.Anchor.ItemIDs[0].String() // an added item: taxed like its first, no deals
	}
	if len(sizes) > 0 {
		var sz *menuview.BuildItemSize
		if sel.SizeID == "" {
			sz = &sizes[0]
		}
		for i := range sizes {
			if sizes[i].SizeID.String() == sel.SizeID {
				sz = &sizes[i]
			}
		}
		if sz == nil {
			p.issues = append(p.issues, Issue{Code: IssueUnknownSize, Message: it.Name + ": that size is gone."})
			return p
		}
		base, p.size = sz.PriceCents, sz.SizeID.String()
		p.mods = append(p.mods, sz.Name)
	} else if sel.SizeID != "" {
		p.issues = append(p.issues, Issue{Code: IssueUnknownSize, Message: it.Name + ": that size is gone."})
		return p
	}
	if soldOut {
		p.issues = append(p.issues, Issue{Code: IssueSoldOut, Message: p.name + " is sold out right now."})
	}
	extras := []int64{}
	for _, gid := range it.ModifierGroupIDs {
		g, ok := e.groups[gid.String()]
		if !ok {
			continue
		}
		picks := []Pick{}
		for _, pk := range sel.Options {
			if pk.GroupID == g.ID.String() {
				picks = append(picks, pk)
			}
		}
		seen := map[string]bool{}
		for _, pk := range picks {
			var o *menuview.BuildOption
			for i := range g.Options {
				if g.Options[i].ID.String() == pk.OptionID {
					o = &g.Options[i]
				}
			}
			if o == nil {
				p.issues = append(p.issues, Issue{Code: IssueUnknownOpt, Message: it.Name + ": an option is no longer available."})
				continue
			}
			if seen[pk.OptionID] && !g.AllowMultiple {
				p.issues = append(p.issues, Issue{Code: IssueOptionRules, Message: it.Name + ": " + o.Name + " only once."})
			}
			seen[pk.OptionID] = true
			if o.SoldOut {
				p.issues = append(p.issues, Issue{Code: IssueSoldOut, Message: o.Name + " is sold out right now."})
			}
			half := ""
			if g.Portions && (pk.Portion == "left" || pk.Portion == "right") {
				half = " (" + pk.Portion + " half)"
			}
			if !o.IsDefault || half != "" {
				p.mods = append(p.mods, o.Name+half)
			}
			price := o.PriceCents
			for _, op := range o.Prices {
				if p.size != "" && op.SizeID.String() == p.size {
					price = op.PriceCents
				}
			}
			if price > 0 {
				extras = append(extras, price)
			}
		}
		// "NO onions": a default left off in a pick-many group (in a pick-one
		// group the choice replaces it).
		if g.MaxSelect != 1 {
			for _, o := range g.Options {
				if o.IsDefault && !seen[o.ID.String()] {
					p.mods = append(p.mods, "NO "+o.Name)
				}
			}
		}
		min := g.MinSelect
		if g.Required && min < 1 {
			min = 1
		}
		if len(picks) < min {
			what := strconv.Itoa(min)
			if min == 1 {
				what = "a"
			}
			p.issues = append(p.issues, Issue{Code: IssueOptionRules, Message: fmt.Sprintf("%s: choose %s %s.", it.Name, what, strings.ToLower(g.Name))})
		}
		if g.MaxSelect > 0 && len(picks) > g.MaxSelect {
			p.issues = append(p.issues, Issue{Code: IssueOptionRules, Message: fmt.Sprintf("%s: up to %d %s.", it.Name, g.MaxSelect, strings.ToLower(g.Name))})
		}
	}
	for _, pk := range sel.Options {
		if !containsGroup(it.ModifierGroupIDs, pk.GroupID) {
			p.issues = append(p.issues, Issue{Code: IssueUnknownOpt, Message: it.Name + ": an option doesn't belong to this item."})
			break
		}
	}
	sort.SliceStable(extras, func(i, j int) bool { return extras[i] > extras[j] })
	for i, x := range extras {
		if i >= free {
			p.extras += x
		}
	}
	p.base = base
	p.unit = base + p.extras
	return p
}

func containsGroup(ids []uuid.UUID, id string) bool {
	for _, g := range ids {
		if g.String() == id {
			return true
		}
	}
	return false
}

// Price prices a cart at `at`.
func Price(ctx Context, cart Cart, at time.Time) Quote {
	e := newEngine(ctx)
	st := ctx.Settings
	q := Quote{Lines: []QuoteLine{}, Discounts: []Discount{}, Errors: []Issue{}, Warnings: []string{}, Nudges: []string{}}
	issue := func(code, msg string, line *int) {
		for _, x := range q.Errors {
			if x.Code == code && x.Message == msg {
				return
			}
		}
		q.Errors = append(q.Errors, Issue{Code: code, Message: msg, Line: line})
	}
	nudge := func(msg string) {
		if !contains(q.Nudges, msg) {
			q.Nudges = append(q.Nudges, msg)
		}
	}
	type taxed struct {
		total int64
		rate  int
	}
	var units []unit
	var lineTax []taxed
	hasBundle := false

	for i, line := range cart.Lines {
		idx := i
		qty := line.Qty
		if qty < 1 {
			qty = 1
		}
		if qty > 50 {
			qty = 50
		}
		if line.DealID == "" {
			p := e.price(line.Selection, 0)
			for _, is := range p.issues {
				issue(is.Code, is.Message, &idx)
			}
			mods := p.mods
			if n := strings.TrimSpace(line.Note); n != "" {
				mods = append(mods, "“"+n+"”")
			}
			q.Lines = append(q.Lines, QuoteLine{Index: i, Name: p.name, Mods: mods, Qty: qty, UnitCents: p.unit, TotalCents: p.unit * int64(qty)})
			lineTax = append(lineTax, taxed{p.unit * int64(qty), e.rate(p.rateItem)})
			if p.bankItem != "" && len(p.issues) == 0 {
				for k := 0; k < qty; k++ {
					units = append(units, unit{price: p.unit, item: p.bankItem, category: e.category(p.bankItem), size: p.size})
				}
			}
			continue
		}
		// A bundle deal.
		hasBundle = true
		var d *Deal
		for k := range ctx.Deals {
			if ctx.Deals[k].ID == line.DealID && ctx.Deals[k].Kind == DealBundle {
				d = &ctx.Deals[k]
			}
		}
		if d == nil || !d.Active {
			issue(IssueDealEnded, "A deal in your order has ended.", &idx)
			q.Lines = append(q.Lines, QuoteLine{Index: i, Name: "Ended deal", Mods: []string{}, Qty: qty, DealID: line.DealID})
			continue
		}
		if !Live(*d, ctx.Timezone, at, cart.Type) {
			issue(IssueDealNotNow, d.Name+" isn't available right now.", &idx)
		}
		var extras, regular int64
		mods := []string{}
		rateItem := ""
		for k, slot := range d.Slots {
			if k >= len(line.Picks) {
				issue(IssueDealSlot, d.Name+": finish every step.", &idx)
				continue
			}
			pick := line.Picks[k]
			p := e.price(pick, slot.IncludedOptions)
			for _, is := range p.issues {
				issue(is.Code, is.Message, &idx)
			}
			if len(p.issues) == 0 && !matches(slot.Eligible, unit{item: p.bankItem, category: e.category(p.bankItem), size: p.size}) {
				issue(IssueDealSlot, fmt.Sprintf("%s: %s doesn't fit “%s”.", d.Name, p.name, slot.Label), &idx)
			}
			if rateItem == "" {
				rateItem = p.rateItem
			}
			extras += p.extras
			regular += e.price(pick, 0).unit
			mods = append(mods, strings.Join(append([]string{p.name}, p.mods...), " · "))
		}
		u := d.PriceCents + extras
		reg := regular
		if regular > u {
			q.SavedCents += (regular - u) * int64(qty)
		}
		q.Lines = append(q.Lines, QuoteLine{Index: i, Name: d.Name, Mods: mods, Qty: qty, UnitCents: u, TotalCents: u * int64(qty), RegularCents: &reg, DealID: d.ID})
		lineTax = append(lineTax, taxed{u * int64(qty), e.rate(rateItem)})
	}
	if len(q.Lines) == 0 {
		issue(IssueEmpty, "Your order is empty.", nil)
	}
	for _, l := range q.Lines {
		q.SubtotalCents += l.TotalCents
	}
	sub := q.SubtotalCents

	// Automatic deals.
	var autos []Discount
	for _, d := range ctx.Deals {
		if !Live(d, ctx.Timezone, at, cart.Type) || d.Eligible == nil {
			continue
		}
		var el []unit
		for _, u := range units {
			if matches(*d.Eligible, u) {
				el = append(el, u)
			}
		}
		var amount int64
		switch d.Kind {
		case DealMix:
			if d.Qty > 0 && len(el) >= d.Qty {
				for _, u := range el {
					if u.price > d.EachCents {
						amount += u.price - d.EachCents
					}
				}
			} else if d.Qty > 0 && len(el) == d.Qty-1 {
				nudge(fmt.Sprintf("Add 1 more for %s: %s each.", d.Name, Money(d.EachCents)))
			}
		case DealNth:
			if d.N < 1 {
				continue
			}
			sort.SliceStable(el, func(i, j int) bool { return el[i].price > el[j].price })
			for k := d.N - 1; k < len(el); k += d.N {
				amount += Pct(el[k].price, d.Pct*100)
			}
			if d.N > 1 && len(el)%d.N == d.N-1 {
				nudge(fmt.Sprintf("Add 1 more for %s.", d.Name))
			}
		case DealFreeItem:
			if len(el) == 0 {
				continue
			}
			if sub >= d.MinSubtotalCents {
				amount = el[0].price
				for _, u := range el {
					if u.price < amount {
						amount = u.price
					}
				}
			} else {
				nudge(fmt.Sprintf("Add %s more for %s.", Money(d.MinSubtotalCents-sub), d.Name))
			}
		}
		if amount > 0 {
			autos = append(autos, Discount{Type: "deal", DealID: d.ID, Label: d.Name, AmountCents: amount})
		}
	}

	// A promo code.
	var code *Discount
	if c := strings.ToUpper(strings.TrimSpace(cart.Code)); c != "" {
		var d *Deal
		for k := range ctx.Deals {
			if ctx.Deals[k].Kind == DealCode && strings.ToUpper(strings.TrimSpace(ctx.Deals[k].Code)) == c {
				d = &ctx.Deals[k]
			}
		}
		switch {
		case d == nil || !Live(*d, ctx.Timezone, at, cart.Type):
			issue(IssueCodeInvalid, "Code "+c+" isn't valid right now.", nil)
		case sub < d.MinSubtotalCents:
			issue(IssueCodeMinimum, fmt.Sprintf("%s needs %s or more.", c, Money(d.MinSubtotalCents)), nil)
		case d.FirstOrderOnly && cart.FirstOrder != nil && !*cart.FirstOrder:
			issue(IssueCodeFirst, c+" is for a first order only.", nil)
		default:
			amt := d.AmountCents
			if d.Pct > 0 {
				amt = Pct(sub, d.Pct*100)
			}
			if amt > sub {
				amt = sub
			}
			code = &Discount{Type: "code", DealID: d.ID, Label: "Code " + c, AmountCents: amt}
		}
	}

	best := func() *Discount {
		var b *Discount
		for k := range autos {
			if b == nil || autos[k].AmountCents > b.AmountCents {
				b = &autos[k]
			}
		}
		return b
	}
	pickOne := func() {
		auto := best()
		switch {
		case auto != nil && code != nil && code.AmountCents >= auto.AmountCents:
			q.Discounts = append(q.Discounts, *code)
			q.Warnings = append(q.Warnings, "Your code saves more than "+auto.Label+", so we used the code.")
		case auto != nil && code != nil:
			q.Discounts = append(q.Discounts, *auto)
			q.Warnings = append(q.Warnings, auto.Label+" saves more than your code, so we kept the deal.")
		case auto != nil:
			q.Discounts = append(q.Discounts, *auto)
		case code != nil:
			q.Discounts = append(q.Discounts, *code)
		}
	}
	switch st.Stacking {
	case StackAll:
		q.Discounts = append(q.Discounts, autos...)
		if code != nil {
			q.Discounts = append(q.Discounts, *code)
		}
	case StackSingle:
		if hasBundle {
			if code != nil {
				issue(IssueCodeAlone, "Codes don't combine with deals.", nil)
			}
		} else {
			pickOne()
		}
	default:
		pickOne()
	}
	// Never more off than the food.
	left := sub
	for k := range q.Discounts {
		if q.Discounts[k].AmountCents > left {
			q.Discounts[k].AmountCents = left
		}
		left -= q.Discounts[k].AmountCents
	}
	after := left

	switch cart.Type {
	case "pickup":
		if !ctx.Types.Pickup {
			issue(IssueTypeOff, "This store isn't taking pickup orders right now.", nil)
		}
	case "delivery":
		if !ctx.Types.Delivery {
			issue(IssueTypeOff, "This store isn't taking delivery orders right now.", nil)
		}
	default:
		issue(IssueBadType, "Choose pickup or delivery.", nil)
	}
	if cart.Type == "delivery" && ctx.Types.Delivery {
		if len(q.Lines) > 0 && after < st.DeliveryMinCents {
			issue(IssueDeliveryMin, fmt.Sprintf("Delivery needs a %s minimum. Add %s more or pick it up.", Money(st.DeliveryMinCents), Money(st.DeliveryMinCents-after)), nil)
		}
		q.DeliveryFeeCents = st.DeliveryFeeCents
		if f := st.FreeDeliveryOverCents; f != nil {
			if after >= *f {
				q.DeliveryFeeCents = 0
			} else if st.DeliveryFeeCents > 0 {
				nudge(fmt.Sprintf("Free delivery over %s: add %s.", Money(*f), Money(*f-after)))
			}
		}
	}

	if st.CardFee.Mode == CardFeeCashDiscount && cart.Payment == "cash" && after > 0 {
		cash := Pct(after, st.CardFee.Bps)
		q.Discounts = append(q.Discounts, Discount{Type: "cash", Label: "Cash discount", AmountCents: cash})
		after -= cash
	}
	if len(q.Lines) > 0 {
		q.ServiceFeeCents = st.ServiceFeeCents + Pct(after, st.ServiceFeeBps)
	}

	// Tax: each rate's share of the food after discounts (the discounts
	// spread over the rates by their share of the subtotal), plus the
	// service fee at the store rate. Delivery is untaxed.
	byRate := map[int]int64{}
	var rates []int
	for _, lt := range lineTax {
		if _, ok := byRate[lt.rate]; !ok {
			rates = append(rates, lt.rate)
		}
		byRate[lt.rate] += lt.total
	}
	sort.Ints(rates)
	off := sub - after
	var spread int64
	for k, r := range rates {
		gross := byRate[r]
		share := int64(0)
		if sub > 0 {
			if k == len(rates)-1 {
				share = off - spread
			} else {
				share = off * gross / sub
			}
		}
		spread += share
		q.TaxCents += Pct(gross-share, r)
	}
	q.TaxCents += Pct(q.ServiceFeeCents, st.TaxBps)

	if st.Tips.Enabled {
		switch {
		case cart.TipCents != nil && *cart.TipCents >= 0:
			q.TipCents = *cart.TipCents
		default:
			pct := st.Tips.DefaultPct
			if cart.TipPct != nil && *cart.TipPct >= 0 && *cart.TipPct <= 100 {
				pct = *cart.TipPct
			}
			q.TipCents = Pct(after, pct*100)
		}
	} else if (cart.TipCents != nil && *cart.TipCents > 0) || (cart.TipPct != nil && *cart.TipPct > 0) {
		q.Warnings = append(q.Warnings, "Tips aren't taken here, so none was added.")
	}
	if st.CardFee.Mode == CardFeeSurcharge && cart.Payment != "cash" {
		q.CardFeeCents = Pct(after+q.DeliveryFeeCents+q.ServiceFeeCents+q.TaxCents, st.CardFee.Bps)
	}
	q.TotalCents = after + q.DeliveryFeeCents + q.ServiceFeeCents + q.TaxCents + q.TipCents + q.CardFeeCents
	for _, d := range q.Discounts {
		q.SavedCents += d.AmountCents
	}
	return q
}
