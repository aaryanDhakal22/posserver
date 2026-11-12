package menuview

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"quiccpos/main/internal/domain/menu"

	"github.com/google/uuid"
)

// The counter layout (TERMINAL-PLAN.md Stage 0a): the counter terminal's
// screen, copied from SoftTouch so staff keep their muscle memory. It lives on
// a counter view (menu_views.layout) and is versioned with the menu.
//
//   - tab_strip + tabs: the menu tabs (the home page's category buttons and the
//     right-hand column), each a fixed grid of buttons over named pages;
//   - screens: the modifier screens (Add Mods / 1/2 Topping / No Mods …), each
//     a grid of option buttons and half-and-half state buttons;
//   - flows: each item's ordered modifier steps (a screen may come twice);
//   - functions + bottom_bar: the function buttons (toolbar sections and the
//     contextual bottom bar per selection state);
//   - quick_notes: one-tap notes (texts, no ids).
//
// It names bank rows by id; every id must exist in the store's bank when it
// is saved. Captions and prices come from the bank in the build
// (ResolveLayout), never stored twice.

// LayoutOf is the layout's shape over its action (A) and flow step (S)
// types: Layout uses ids, the bank document (bankdoc) names, the build adds
// what the Pi needs (BuildLayout).
type LayoutOf[A any, S any] struct {
	TabStrip  Grid           `json:"tab_strip"`
	Tabs      []TabOf[A]     `json:"tabs"`
	Screens   []ScreenOf[A]  `json:"screens"`
	Flows     map[string][]S `json:"flows"` // item → its modifier steps, in order
	Functions Functions      `json:"functions"`
	BottomBar BottomBar      `json:"bottom_bar"`
	// QuickNotes are the store's one-tap notes (SoftTouch's quick messages):
	// plain texts the terminal offers for a line or the order, in order.
	QuickNotes []string `json:"quick_notes"`
}

// Layout is a counter layout as stored: flows are keyed by bank item id.
type Layout = LayoutOf[Action, Step]

// Grid is a grid's size.
type Grid struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

// Page is one page of a grid (SoftTouch's page tabs: "12\" Med", "Add Mods").
type Page struct {
	Name string `json:"name"`
}

// TabOf is one menu tab: its cell on the tab strip, its style and its grid.
type TabOf[A any] struct {
	Name      string        `json:"name"`
	StripRow  int           `json:"strip_row"`
	StripCol  int           `json:"strip_col"`
	Color     string        `json:"color"`
	FontColor string        `json:"font_color"`
	FontSize  int           `json:"font_size"`
	Grid      Grid          `json:"grid"`
	Pages     []Page        `json:"pages"`
	Buttons   []ButtonOf[A] `json:"buttons"`
}

// ScreenOf is one modifier screen.
type ScreenOf[A any] struct {
	Name    string        `json:"name"`
	Prompt  string        `json:"prompt"`
	Grid    Grid          `json:"grid"`
	Pages   []Page        `json:"pages"`
	Buttons []ButtonOf[A] `json:"buttons"`
}

// ButtonOf is one 1×1 button on a page of a grid. An empty caption shows the
// bank's name for what it points at.
type ButtonOf[A any] struct {
	Page      int    `json:"page"`
	Row       int    `json:"row"`
	Col       int    `json:"col"`
	Caption   string `json:"caption"`
	Color     string `json:"color"`
	FontColor string `json:"font_color"`
	FontSize  int    `json:"font_size"`
	Action    A      `json:"action"`
}

// Action types: a tab button sells an item (+ size), opens a category or a
// deal, or runs a function; a screen button picks an option, sets the
// half-and-half state, or picks an item (a combo's drink).
const (
	ActItem     = "item"
	ActCategory = "category"
	ActDeal     = "deal"
	ActFunction = "function"
	ActOption   = "option"
	ActState    = "state"
	ActItemPick = "item_pick"
)

// States of a screen's state buttons (WHOLE, 1ST HALF ADD/ONLY, 2ND HALF
// ADD/ONLY, NO).
var States = []string{"whole", "first_add", "first_only", "second_add", "second_only", "no"}

// Action is what a button does, by bank id.
type Action struct {
	Type       string     `json:"type"`
	ItemID     *uuid.UUID `json:"item_id,omitempty"`
	SizeID     *uuid.UUID `json:"size_id,omitempty"`
	CategoryID *uuid.UUID `json:"category_id,omitempty"`
	DealID     string     `json:"deal_id,omitempty"`
	Name       string     `json:"name,omitempty"` // function
	GroupID    *uuid.UUID `json:"group_id,omitempty"`
	OptionID   *uuid.UUID `json:"option_id,omitempty"`
	State      string     `json:"state,omitempty"`
	// Mode is an option button's mode: add ("" too) | no — a "No Onion"
	// button picks Onion with mode no (TERMINAL-PLAN 0d).
	Mode string `json:"mode,omitempty"`
}

// Option button modes.
var OptionModes = []string{"add", "no"}

// Step is one modifier step of an item: the screen to show, how many picks
// it needs (0 = optional), whether it can't be skipped, the prompt. SizeID
// limits the step to that size of the item (SoftTouch had one item per size,
// each with its own screens); nil = every size.
type Step struct {
	Screen        string     `json:"screen"`
	SizeID        *uuid.UUID `json:"size_id,omitempty"`
	RequiredCount int        `json:"required_count"`
	Forced        bool       `json:"forced"`
	Prompt        string     `json:"prompt"`
}

// Functions are the function buttons by section: nav (the top toolbar),
// employee and manager (the screens behind Employee / System).
type Functions struct {
	Nav      []Function `json:"nav"`
	Employee []Function `json:"employee"`
	Manager  []Function `json:"manager"`
}

// Function is one function button. Role is who may use it ("" = anyone).
type Function struct {
	Name    string `json:"name"`
	Caption string `json:"caption"`
	Order   int    `json:"order"`
	Visible bool   `json:"visible"`
	Role    string `json:"role"`
}

// Roles a function may require.
var Roles = []string{"", "cashier", "manager", "driver"}

// BottomBar is the contextual bottom bar per selection state: nothing
// selected (none), an item line (line), a modifier line (modifier).
type BottomBar struct {
	None     Bar `json:"none"`
	Line     Bar `json:"line"`
	Modifier Bar `json:"modifier"`
}

// Bar is one state's bar: a small grid of function buttons (0×0 = none).
type Bar struct {
	Rows    int         `json:"rows"`
	Cols    int         `json:"cols"`
	Buttons []BarButton `json:"buttons"`
}

// BarButton is a function on the bottom bar; Emphasis draws it highlighted
// (SoftTouch's tan buttons).
type BarButton struct {
	Row      int    `json:"row"`
	Col      int    `json:"col"`
	Name     string `json:"name"`
	Caption  string `json:"caption"`
	Emphasis bool   `json:"emphasis"`
}

// Limits of a layout.
const (
	MaxGridSide     = 12
	MaxPages        = 16
	MaxTabs         = 64
	MaxScreens      = 500
	MaxFlows        = 5000
	MaxSteps        = 20
	MaxFunctions    = 64
	MaxBarRows      = 4
	MaxRequired     = 99
	MaxNameLen      = 60
	MaxCaptionLen   = 80
	MaxPromptLen    = 120
	MaxPageNameLen  = 40
	MinFontSize     = 6
	MaxFontSize     = 72
	MaxProblems     = 100
	MaxQuickNotes   = 50
	MaxQuickNoteLen = 80
	maxFunctionName = 40
)

// Problem is one thing wrong with a layout, at a JSON path.
type Problem struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

var (
	colorRe = regexp.MustCompile(`^#[0-9a-f]{6}$`)
	funcRe  = regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`)
)

// NormalizeLayout trims names, lower-cases colours and turns nil lists into
// empty ones, so a saved layout reads back (and round-trips) the same.
func NormalizeLayout[A, S any](l *LayoutOf[A, S]) {
	if l.Tabs == nil {
		l.Tabs = []TabOf[A]{}
	}
	if l.Screens == nil {
		l.Screens = []ScreenOf[A]{}
	}
	if l.Flows == nil {
		l.Flows = map[string][]S{}
	}
	for k, v := range l.Flows {
		if v == nil {
			l.Flows[k] = []S{}
		}
	}
	for i := range l.Tabs {
		t := &l.Tabs[i]
		t.Name = strings.TrimSpace(t.Name)
		t.Color, t.FontColor = normColor(t.Color), normColor(t.FontColor)
		normPages(&t.Pages)
		normButtons(t.Buttons)
		if t.Buttons == nil {
			t.Buttons = []ButtonOf[A]{}
		}
	}
	for i := range l.Screens {
		s := &l.Screens[i]
		s.Name, s.Prompt = strings.TrimSpace(s.Name), strings.TrimSpace(s.Prompt)
		normPages(&s.Pages)
		normButtons(s.Buttons)
		if s.Buttons == nil {
			s.Buttons = []ButtonOf[A]{}
		}
	}
	for _, fs := range []*[]Function{&l.Functions.Nav, &l.Functions.Employee, &l.Functions.Manager} {
		if *fs == nil {
			*fs = []Function{}
		}
		for i := range *fs {
			f := &(*fs)[i]
			f.Name, f.Caption, f.Role = strings.TrimSpace(f.Name), strings.TrimSpace(f.Caption), strings.TrimSpace(f.Role)
		}
	}
	if l.QuickNotes == nil {
		l.QuickNotes = []string{}
	}
	for i := range l.QuickNotes {
		l.QuickNotes[i] = strings.TrimSpace(l.QuickNotes[i])
	}
	for _, b := range []*Bar{&l.BottomBar.None, &l.BottomBar.Line, &l.BottomBar.Modifier} {
		if b.Buttons == nil {
			b.Buttons = []BarButton{}
		}
		for i := range b.Buttons {
			x := &b.Buttons[i]
			x.Name, x.Caption = strings.TrimSpace(x.Name), strings.TrimSpace(x.Caption)
		}
	}
}

func normColor(c string) string { return strings.ToLower(strings.TrimSpace(c)) }

func normPages(p *[]Page) {
	if *p == nil {
		*p = []Page{}
	}
	for i := range *p {
		(*p)[i].Name = strings.TrimSpace((*p)[i].Name)
	}
}

func normButtons[A any](bs []ButtonOf[A]) {
	for i := range bs {
		b := &bs[i]
		b.Caption = strings.TrimSpace(b.Caption)
		b.Color, b.FontColor = normColor(b.Color), normColor(b.FontColor)
	}
}

// LayoutRefs is what a layout may point at: the store's bank and its deals
// (id → name).
type LayoutRefs struct {
	Bank  *menu.Bank
	Deals map[string]string
}

type problems struct{ list []Problem }

func (p *problems) add(path, format string, a ...any) {
	if len(p.list) < MaxProblems {
		p.list = append(p.list, Problem{Path: path, Message: fmt.Sprintf(format, a...)})
	}
}

func (p *problems) text(path, kind, s string, max int, required bool) {
	switch {
	case required && s == "":
		p.add(path, "%s is required", kind)
	case utf8.RuneCountInString(s) > max:
		p.add(path, "%s is longer than %d characters", kind, max)
	case strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		p.add(path, "%s has control characters", kind)
	}
}

func (p *problems) color(path, c string) {
	if c != "" && !colorRe.MatchString(c) {
		p.add(path, "colour must be #rrggbb")
	}
}

func (p *problems) fontSize(path string, n int) {
	if n != 0 && (n < MinFontSize || n > MaxFontSize) {
		p.add(path, "font size must be 0 (default) or %d..%d", MinFontSize, MaxFontSize)
	}
}

func (p *problems) grid(path string, g Grid, maxRows, maxCols int) bool {
	ok := true
	if g.Rows < 1 || g.Rows > maxRows {
		p.add(path+".rows", "rows must be 1..%d", maxRows)
		ok = false
	}
	if g.Cols < 1 || g.Cols > maxCols {
		p.add(path+".cols", "cols must be 1..%d", maxCols)
		ok = false
	}
	return ok
}

// ValidateLayout checks a (normalized) layout against the store: shape, limits,
// grids and pages, colours, and that every id exists in refs. It returns
// every problem found (up to MaxProblems), nil when there is none.
func ValidateLayout(l *Layout, refs LayoutRefs) []Problem {
	p := &problems{}
	v := layoutValidator{p: p, refs: refs}
	v.index()
	v.run(l)
	return p.list
}

type layoutValidator struct {
	p     *problems
	refs  LayoutRefs
	items map[uuid.UUID]*menu.BankItem
	cats  map[uuid.UUID]bool
}

func (v *layoutValidator) index() {
	v.items, v.cats = map[uuid.UUID]*menu.BankItem{}, map[uuid.UUID]bool{}
	if v.refs.Bank == nil {
		return
	}
	for mi := range v.refs.Bank.Menus {
		for ci := range v.refs.Bank.Menus[mi].Categories {
			c := &v.refs.Bank.Menus[mi].Categories[ci]
			v.cats[c.ID] = true
			for ii := range c.Items {
				v.items[c.Items[ii].ID] = &c.Items[ii]
			}
		}
	}
}

func (v *layoutValidator) run(l *Layout) {
	p := v.p
	stripOK := p.grid("tab_strip", l.TabStrip, MaxGridSide, MaxGridSide)
	if len(l.Tabs) > MaxTabs {
		p.add("tabs", "at most %d tabs", MaxTabs)
	}
	names, cells := map[string]string{}, map[[2]int]string{}
	for i, t := range l.Tabs {
		path := fmt.Sprintf("tabs[%d]", i)
		p.text(path+".name", "name", t.Name, MaxNameLen, true)
		if t.Name != "" {
			if prev, dup := names[normKey(t.Name)]; dup {
				p.add(path+".name", "tab %q is already at %s", t.Name, prev)
			} else {
				names[normKey(t.Name)] = path
			}
		}
		if stripOK {
			if t.StripRow < 0 || t.StripRow >= l.TabStrip.Rows || t.StripCol < 0 || t.StripCol >= l.TabStrip.Cols {
				p.add(path, "strip cell (%d,%d) is outside the %d×%d tab strip", t.StripRow, t.StripCol, l.TabStrip.Rows, l.TabStrip.Cols)
			} else if prev, dup := cells[[2]int{t.StripRow, t.StripCol}]; dup {
				p.add(path, "strip cell (%d,%d) is taken by %s", t.StripRow, t.StripCol, prev)
			} else {
				cells[[2]int{t.StripRow, t.StripCol}] = path
			}
		}
		p.color(path+".color", t.Color)
		p.color(path+".font_color", t.FontColor)
		p.fontSize(path+".font_size", t.FontSize)
		v.gridButtons(path, t.Grid, t.Pages, t.Buttons, false)
	}
	if len(l.Screens) > MaxScreens {
		p.add("screens", "at most %d screens", MaxScreens)
	}
	screens := map[string]string{}
	for i, s := range l.Screens {
		path := fmt.Sprintf("screens[%d]", i)
		p.text(path+".name", "name", s.Name, MaxNameLen, true)
		if s.Name != "" {
			if prev, dup := screens[normKey(s.Name)]; dup {
				p.add(path+".name", "screen %q is already at %s", s.Name, prev)
			} else {
				screens[normKey(s.Name)] = path
			}
		}
		p.text(path+".prompt", "prompt", s.Prompt, MaxPromptLen, false)
		v.gridButtons(path, s.Grid, s.Pages, s.Buttons, true)
	}
	if len(l.Flows) > MaxFlows {
		p.add("flows", "at most %d items with flows", MaxFlows)
	}
	keys := make([]string, 0, len(l.Flows))
	for k := range l.Flows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		path := "flows." + k
		id, err := uuid.Parse(k)
		var it *menu.BankItem
		if err != nil {
			p.add(path, "key must be an item id")
		} else if it = v.items[id]; it == nil {
			p.add(path, "item %s is not in this store's bank", k)
		}
		steps := l.Flows[k]
		if len(steps) > MaxSteps {
			p.add(path, "at most %d steps", MaxSteps)
		}
		for j, st := range steps {
			sp := fmt.Sprintf("%s[%d]", path, j)
			if st.Screen == "" {
				p.add(sp+".screen", "screen is required")
			} else if _, ok := screens[normKey(st.Screen)]; !ok {
				p.add(sp+".screen", "no screen named %q", st.Screen)
			}
			if st.SizeID != nil && it != nil && !itemHasSize(it, *st.SizeID) {
				p.add(sp+".size_id", "size %s is not one of this item's sizes", *st.SizeID)
			}
			if st.RequiredCount < 0 || st.RequiredCount > MaxRequired {
				p.add(sp+".required_count", "required_count must be 0..%d", MaxRequired)
			}
			p.text(sp+".prompt", "prompt", st.Prompt, MaxPromptLen, false)
		}
	}
	for _, sec := range []struct {
		name string
		fs   []Function
	}{{"nav", l.Functions.Nav}, {"employee", l.Functions.Employee}, {"manager", l.Functions.Manager}} {
		path := "functions." + sec.name
		if len(sec.fs) > MaxFunctions {
			p.add(path, "at most %d functions", MaxFunctions)
		}
		seen := map[string]bool{}
		for i, f := range sec.fs {
			fp := fmt.Sprintf("%s[%d]", path, i)
			v.funcName(fp+".name", f.Name)
			if seen[f.Name] {
				p.add(fp+".name", "function %q is listed twice", f.Name)
			}
			seen[f.Name] = true
			p.text(fp+".caption", "caption", f.Caption, MaxCaptionLen, false)
			if f.Order < 0 || f.Order > 999 {
				p.add(fp+".order", "order must be 0..999")
			}
			if !validRole(f.Role) {
				p.add(fp+".role", "role must be empty, cashier, manager or driver")
			}
		}
	}
	for _, bar := range []struct {
		name string
		b    Bar
	}{{"none", l.BottomBar.None}, {"line", l.BottomBar.Line}, {"modifier", l.BottomBar.Modifier}} {
		path := "bottom_bar." + bar.name
		b := bar.b
		if b.Rows == 0 && b.Cols == 0 {
			if len(b.Buttons) > 0 {
				p.add(path, "a bar with buttons needs rows and cols")
			}
			continue
		}
		if !p.grid(path, Grid{Rows: b.Rows, Cols: b.Cols}, MaxBarRows, MaxGridSide) {
			continue
		}
		taken := map[[2]int]int{}
		for i, x := range b.Buttons {
			bp := fmt.Sprintf("%s.buttons[%d]", path, i)
			v.funcName(bp+".name", x.Name)
			p.text(bp+".caption", "caption", x.Caption, MaxCaptionLen, false)
			if x.Row < 0 || x.Row >= b.Rows || x.Col < 0 || x.Col >= b.Cols {
				p.add(bp, "cell (%d,%d) is outside the %d×%d bar", x.Row, x.Col, b.Rows, b.Cols)
				continue
			}
			if prev, dup := taken[[2]int{x.Row, x.Col}]; dup {
				p.add(bp, "cell (%d,%d) overlaps buttons[%d]", x.Row, x.Col, prev)
				continue
			}
			taken[[2]int{x.Row, x.Col}] = i
		}
	}
	if len(l.QuickNotes) > MaxQuickNotes {
		p.add("quick_notes", "at most %d quick notes", MaxQuickNotes)
	}
	seenNote := map[string]int{}
	for i, n := range l.QuickNotes {
		np := fmt.Sprintf("quick_notes[%d]", i)
		p.text(np, "a quick note", n, MaxQuickNoteLen, true)
		if prev, dup := seenNote[strings.ToLower(n)]; dup && n != "" {
			p.add(np, "the same note as quick_notes[%d]", prev)
		} else {
			seenNote[strings.ToLower(n)] = i
		}
	}
}

// gridButtons checks a grid, its pages and its buttons (in the grid, one per
// cell per page, each action valid where it is).
func (v *layoutValidator) gridButtons(path string, g Grid, pages []Page, buttons []ButtonOf[Action], screen bool) {
	p := v.p
	gridOK := p.grid(path+".grid", g, MaxGridSide, MaxGridSide)
	if len(pages) < 1 || len(pages) > MaxPages {
		p.add(path+".pages", "1..%d pages", MaxPages)
	}
	for i, pg := range pages {
		p.text(fmt.Sprintf("%s.pages[%d].name", path, i), "page name", pg.Name, MaxPageNameLen, true)
	}
	taken := map[[3]int]int{}
	for i, b := range buttons {
		bp := fmt.Sprintf("%s.buttons[%d]", path, i)
		switch {
		case b.Page < 0 || b.Page >= len(pages):
			p.add(bp+".page", "page %d doesn't exist (%d pages)", b.Page, len(pages))
		case !gridOK:
		case b.Row < 0 || b.Row >= g.Rows || b.Col < 0 || b.Col >= g.Cols:
			p.add(bp, "cell (%d,%d) is outside the %d×%d grid", b.Row, b.Col, g.Rows, g.Cols)
		default:
			k := [3]int{b.Page, b.Row, b.Col}
			if prev, dup := taken[k]; dup {
				p.add(bp, "cell (%d,%d) on page %d overlaps buttons[%d]", b.Row, b.Col, b.Page, prev)
			} else {
				taken[k] = i
			}
		}
		p.text(bp+".caption", "caption", b.Caption, MaxCaptionLen, false)
		p.color(bp+".color", b.Color)
		p.color(bp+".font_color", b.FontColor)
		p.fontSize(bp+".font_size", b.FontSize)
		v.action(bp+".action", b.Action, screen)
	}
}

func (v *layoutValidator) action(path string, a Action, screen bool) {
	p := v.p
	allowed := map[bool][]string{false: {ActItem, ActCategory, ActDeal, ActFunction}, true: {ActOption, ActState, ActItemPick}}[screen]
	ok := false
	for _, t := range allowed {
		ok = ok || t == a.Type
	}
	if !ok {
		p.add(path+".type", "type must be one of %s", strings.Join(allowed, ", "))
		return
	}
	extra := func(field string, set bool) {
		if set {
			p.add(path+"."+field, "%s doesn't belong on a %s action", field, a.Type)
		}
	}
	if a.Type == ActOption {
		if a.Mode != "" && a.Mode != "add" && a.Mode != "no" {
			p.add(path+".mode", "mode must be one of %s", strings.Join(OptionModes, ", "))
		}
	} else {
		extra("mode", a.Mode != "")
	}
	switch a.Type {
	case ActItem:
		extra("category_id", a.CategoryID != nil)
		v.item(path, a.ItemID, a.SizeID)
	case ActItemPick:
		if (a.ItemID == nil) == (a.CategoryID == nil) {
			p.add(path, "an item_pick needs item_id or category_id (not both)")
			return
		}
		if a.CategoryID != nil {
			extra("size_id", a.SizeID != nil)
			v.category(path+".category_id", a.CategoryID)
		} else {
			v.item(path, a.ItemID, a.SizeID)
		}
	case ActCategory:
		extra("item_id", a.ItemID != nil)
		extra("size_id", a.SizeID != nil)
		v.category(path+".category_id", a.CategoryID)
	case ActDeal:
		if a.DealID == "" {
			p.add(path+".deal_id", "deal_id is required")
		} else if _, ok := v.refs.Deals[a.DealID]; !ok {
			p.add(path+".deal_id", "deal %s is not one of this store's deals", a.DealID)
		}
	case ActFunction:
		v.funcName(path+".name", a.Name)
	case ActOption:
		if a.GroupID == nil || a.OptionID == nil {
			p.add(path, "an option needs group_id and option_id")
			return
		}
		var g menu.BankGroup
		found := false
		if v.refs.Bank != nil {
			g, found = v.refs.Bank.Groups[*a.GroupID]
		}
		if !found {
			p.add(path+".group_id", "modifier group %s is not in this store's bank", *a.GroupID)
			return
		}
		for _, o := range g.Options {
			if o.ID == *a.OptionID {
				return
			}
		}
		p.add(path+".option_id", "option %s is not in group %q", *a.OptionID, g.Name)
	case ActState:
		if !validState(a.State) {
			p.add(path+".state", "state must be one of %s", strings.Join(States, ", "))
		}
	}
	if a.Type != ActOption {
		extra("group_id", a.GroupID != nil)
		extra("option_id", a.OptionID != nil)
	}
	if a.Type != ActState {
		extra("state", a.State != "")
	}
	if a.Type != ActFunction {
		extra("name", a.Name != "")
	}
	if a.Type != ActDeal {
		extra("deal_id", a.DealID != "")
	}
}

// item checks an item reference: it exists; a sized item needs one of its
// sizes, an unsized one takes none.
func (v *layoutValidator) item(path string, itemID, sizeID *uuid.UUID) {
	p := v.p
	if itemID == nil {
		p.add(path+".item_id", "item_id is required")
		return
	}
	it := v.items[*itemID]
	if it == nil {
		p.add(path+".item_id", "item %s is not in this store's bank", *itemID)
		return
	}
	switch {
	case len(it.Sizes) > 0 && sizeID == nil:
		p.add(path+".size_id", "%q comes in sizes: size_id is required", it.Name)
	case len(it.Sizes) == 0 && sizeID != nil:
		p.add(path+".size_id", "%q has no sizes", it.Name)
	case sizeID != nil && !itemHasSize(it, *sizeID):
		p.add(path+".size_id", "size %s is not one of %q's sizes", *sizeID, it.Name)
	}
}

func (v *layoutValidator) category(path string, id *uuid.UUID) {
	if id == nil {
		v.p.add(path, "category_id is required")
	} else if !v.cats[*id] {
		v.p.add(path, "category %s is not in this store's bank", *id)
	}
}

func (v *layoutValidator) funcName(path, name string) {
	switch {
	case name == "":
		v.p.add(path, "function name is required")
	case len(name) > maxFunctionName || !funcRe.MatchString(name):
		v.p.add(path, "function name must be lower case letters, digits, _ . - (≤ %d)", maxFunctionName)
	}
}

func itemHasSize(it *menu.BankItem, sizeID uuid.UUID) bool {
	for _, z := range it.Sizes {
		if z.SizeID == sizeID {
			return true
		}
	}
	return false
}

func validState(s string) bool {
	for _, x := range States {
		if x == s {
			return true
		}
	}
	return false
}

func validRole(r string) bool {
	for _, x := range Roles {
		if x == r {
			return true
		}
	}
	return false
}

// normKey is how screen and tab names match: case and spacing ignored.
func normKey(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// MapLayout copies a layout into another action / step type (ids ↔ names,
// the build's resolved actions). key maps a flow key; it and the action /
// step mappers may refuse with an error, which stops the copy. Flow keys are
// visited in sorted order so the first error is stable.
func MapLayout[A, S, A2, S2 any](l LayoutOf[A, S], key func(path, k string) (string, error),
	act func(path string, a A) (A2, error), step func(path string, s S) (S2, error)) (LayoutOf[A2, S2], error) {
	out := LayoutOf[A2, S2]{TabStrip: l.TabStrip, Functions: l.Functions, BottomBar: l.BottomBar, QuickNotes: l.QuickNotes,
		Tabs: make([]TabOf[A2], 0, len(l.Tabs)), Screens: make([]ScreenOf[A2], 0, len(l.Screens)), Flows: map[string][]S2{}}
	buttons := func(path string, bs []ButtonOf[A]) ([]ButtonOf[A2], error) {
		o := make([]ButtonOf[A2], 0, len(bs))
		for i, b := range bs {
			a, err := act(fmt.Sprintf("%s.buttons[%d].action", path, i), b.Action)
			if err != nil {
				return nil, err
			}
			o = append(o, ButtonOf[A2]{Page: b.Page, Row: b.Row, Col: b.Col, Caption: b.Caption, Color: b.Color,
				FontColor: b.FontColor, FontSize: b.FontSize, Action: a})
		}
		return o, nil
	}
	for i, t := range l.Tabs {
		bs, err := buttons(fmt.Sprintf("tabs[%d]", i), t.Buttons)
		if err != nil {
			return out, err
		}
		out.Tabs = append(out.Tabs, TabOf[A2]{Name: t.Name, StripRow: t.StripRow, StripCol: t.StripCol, Color: t.Color,
			FontColor: t.FontColor, FontSize: t.FontSize, Grid: t.Grid, Pages: t.Pages, Buttons: bs})
	}
	for i, s := range l.Screens {
		bs, err := buttons(fmt.Sprintf("screens[%d]", i), s.Buttons)
		if err != nil {
			return out, err
		}
		out.Screens = append(out.Screens, ScreenOf[A2]{Name: s.Name, Prompt: s.Prompt, Grid: s.Grid, Pages: s.Pages, Buttons: bs})
	}
	keys := make([]string, 0, len(l.Flows))
	for k := range l.Flows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		path := "flows." + k
		nk, err := key(path, k)
		if err != nil {
			return out, err
		}
		if _, dup := out.Flows[nk]; dup {
			return out, fmt.Errorf("%s: names an item twice (%q)", path, nk)
		}
		steps := make([]S2, 0, len(l.Flows[k]))
		for j, s := range l.Flows[k] {
			s2, err := step(fmt.Sprintf("%s[%d]", path, j), s)
			if err != nil {
				return out, err
			}
			steps = append(steps, s2)
		}
		out.Flows[nk] = steps
	}
	return out, nil
}
