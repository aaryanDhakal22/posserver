package menuview

import (
	"regexp"
	"strings"

	"quiccpos/main/internal/domain/menu"

	"github.com/google/uuid"
)

// ButtonRef names one button of a counter layout the way a needs-attention
// line says where it is: its tab or screen (kind + name) and its cell.
type ButtonRef struct {
	WhereKind string `json:"where_kind"` // tab | screen
	Where     string `json:"where"`
	Page      int    `json:"page"`
	Row       int    `json:"row"`
	Col       int    `json:"col"`
}

// RemoveFromLayout takes the named buttons and flows (bank item ids) out of a
// layout, and with them the flow of every item the bank no longer has that no
// button sells any more (a deleted item's flow goes with its last button).
// A name or cell that matches nothing is skipped. It returns how many
// buttons and flows it removed.
func RemoveFromLayout(l *Layout, bank *menu.Bank, buttons []ButtonRef, flows []string) (int, int) {
	drop := map[string]map[[3]int]bool{}
	for _, r := range buttons {
		k := strings.ToLower(strings.TrimSpace(r.WhereKind)) + "|" + normKey(r.Where)
		if drop[k] == nil {
			drop[k] = map[[3]int]bool{}
		}
		drop[k][[3]int{r.Page, r.Row, r.Col}] = true
	}
	removed := 0
	keep := func(kind, name string, bs []ButtonOf[Action]) []ButtonOf[Action] {
		cells := drop[kind+"|"+normKey(name)]
		if len(cells) == 0 {
			return bs
		}
		out := bs[:0]
		for _, b := range bs {
			if cells[[3]int{b.Page, b.Row, b.Col}] {
				removed++
				continue
			}
			out = append(out, b)
		}
		return out
	}
	for i := range l.Tabs {
		l.Tabs[i].Buttons = keep("tab", l.Tabs[i].Name, l.Tabs[i].Buttons)
	}
	for i := range l.Screens {
		l.Screens[i].Buttons = keep("screen", l.Screens[i].Name, l.Screens[i].Buttons)
	}

	flowsGone := 0
	for _, k := range flows {
		if _, ok := l.Flows[k]; ok {
			delete(l.Flows, k)
			flowsGone++
		}
	}
	if len(buttons) > 0 && bank != nil {
		sold := map[string]bool{}
		mark := func(bs []ButtonOf[Action]) {
			for _, b := range bs {
				if b.Action.ItemID != nil {
					sold[b.Action.ItemID.String()] = true
				}
			}
		}
		for _, t := range l.Tabs {
			mark(t.Buttons)
		}
		for _, s := range l.Screens {
			mark(s.Buttons)
		}
		for k := range l.Flows {
			id, err := uuid.Parse(k)
			if err != nil || sold[k] {
				continue
			}
			if _, _, found := bank.Item(id); !found {
				delete(l.Flows, k)
				flowsGone++
			}
		}
	}
	return removed, flowsGone
}

var buttonIndex = regexp.MustCompile(`buttons\[\d+\]`)

// NoNewProblems: every problem in after is one before already had (by
// message, a button's index left out: removing buttons renumbers the rest).
// A layout that only lost buttons or flows can be saved with the problems it
// already had.
func NoNewProblems(before, after []Problem) bool {
	had := map[string]int{}
	for _, p := range before {
		had[buttonIndex.ReplaceAllString(p.Path, "buttons[]")+"|"+buttonIndex.ReplaceAllString(p.Message, "buttons[]")]++
	}
	for _, p := range after {
		k := buttonIndex.ReplaceAllString(p.Path, "buttons[]") + "|" + buttonIndex.ReplaceAllString(p.Message, "buttons[]")
		if had[k] == 0 {
			return false
		}
		had[k]--
	}
	return true
}
