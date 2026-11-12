package bankimport

import (
	"fmt"
	"slices"
	"strings"

	"quiccpos/main/internal/domain/bankdoc"

	"github.com/google/uuid"
)

// Renames (MENU-PLAN M1): a document row finds its store row by its own name
// first (as always), else by one of its `was` names; the row it finds that
// way is renamed in place (same id). Refused (400 bad_document, with the
// path): a `was` name that names two store rows, `was` names that point at
// two different rows, a store row two document rows claim (two `was`, or a
// `was` and another row's own name), and a `was` that names a row of
// another kind (or, for an option, of another group) while naming none of
// its own.

// docRow is a document row as the matcher sees it.
type docRow struct {
	path string
	name string
	was  []string
}

// match is the store row a document row matched; from is the row's name
// before a rename through `was` ("" = matched by its own name).
type match struct {
	id   uuid.UUID
	from string
}

// matcher matches one kind's document rows to its store rows.
type matcher struct {
	kind  string                 // "item", "option" … for messages
	rows  map[string][]uuid.UUID // norm name → store rows, preferred first
	names map[uuid.UUID]string
	// other says what else a name names ("a category", `an option of group
	// "Sauce"`), for a `was` that names nothing of this kind; "" = nothing.
	other func(norm string) string
}

func newMatcher(kind string, other func(string) string) *matcher {
	if other == nil {
		other = func(string) string { return "" }
	}
	return &matcher{kind: kind, rows: map[string][]uuid.UUID{}, names: map[uuid.UUID]string{}, other: other}
}

// add adds a store row (call in preference order: the first row of a name
// is the one a document row's own name matches).
func (m *matcher) add(id uuid.UUID, name string) {
	n := bankdoc.Norm(name)
	m.rows[n] = append(m.rows[n], id)
	m.names[id] = name
}

// resolve matches every document row (nil = a new row).
func (m *matcher) resolve(docs []docRow) ([]*match, error) {
	out := make([]*match, len(docs))
	claimed := map[uuid.UUID]string{}
	for i, d := range docs {
		if ids := m.rows[bankdoc.Norm(d.name)]; len(ids) > 0 {
			out[i] = &match{id: ids[0]}
			claimed[ids[0]] = d.path
		}
	}
	for i, d := range docs {
		if out[i] != nil || len(d.was) == 0 {
			continue
		}
		var found []uuid.UUID
		var via []string
		for j, w := range d.was {
			ids := m.rows[bankdoc.Norm(w)]
			if len(ids) > 1 {
				return nil, bad(fmt.Sprintf("%s.was[%d]", d.path, j), "was %q matches %d %ss of this store", w, len(ids), m.kind)
			}
			if len(ids) == 1 && !slices.Contains(found, ids[0]) {
				found = append(found, ids[0])
				via = append(via, w)
			}
		}
		switch len(found) {
		case 0:
			for j, w := range d.was {
				if what := m.other(bankdoc.Norm(w)); what != "" {
					return nil, bad(fmt.Sprintf("%s.was[%d]", d.path, j), "was %q names %s, not %s %s", w, what, article(m.kind), m.kind)
				}
			}
		case 1:
			if prev, ok := claimed[found[0]]; ok {
				return nil, bad(d.path+".was", "%s %q is also matched by %s", m.kind, m.names[found[0]], prev)
			}
			claimed[found[0]] = d.path
			out[i] = &match{id: found[0], from: m.names[found[0]]}
		default:
			return nil, bad(d.path+".was", "was names two different %ss (%q and %q)", m.kind, via[0], via[1])
		}
	}
	return out, nil
}

func article(kind string) string {
	if strings.ContainsRune("aeiou", rune(kind[0])) {
		return "an"
	}
	return "a"
}

// otherKinds says which other kind of the store's bank a name names, for
// the wrong-kind check: sizes, groups, menus, categories and items.
func (a *applier) otherKinds(not string) func(string) string {
	return func(n string) string {
		for _, k := range []string{"size", "group", "menu", "category", "item"} {
			if k != not && a.names[k][n] {
				return article(k) + " " + k
			}
		}
		return ""
	}
}

// indexNames records the store's names per kind (before anything is written).
func (a *applier) indexNames() {
	a.names = map[string]map[string]bool{"size": {}, "group": {}, "menu": {}, "category": {}, "item": {}}
	for _, z := range a.bank.Sizes {
		a.names["size"][bankdoc.Norm(z.Name)] = true
	}
	for _, g := range a.bank.Groups {
		a.names["group"][bankdoc.Norm(g.Name)] = true
	}
	for _, m := range a.bank.Menus {
		a.names["menu"][bankdoc.Norm(m.Name)] = true
		for _, c := range m.Categories {
			a.names["category"][bankdoc.Norm(c.Name)] = true
			for _, it := range c.Items {
				a.names["item"][bankdoc.Norm(it.Name)] = true
			}
		}
	}
}

// changed reports an existing row's update: a rename through `was` is its
// own line (`item "Old" renamed to "New"`), the other fields as usual.
func (a *applier) changed(kind, what string, fields []string, m *match, renamed string) {
	if m == nil || m.from == "" {
		a.change(kind, what, false, fields)
		return
	}
	a.res.Counts[kind].Updated++
	a.res.Changes = append(a.res.Changes, renamed)
	fields = slices.DeleteFunc(slices.Clone(fields), func(f string) bool { return f == "name" })
	if len(fields) > 0 {
		a.res.Changes = append(a.res.Changes, what+": "+strings.Join(fields, ", "))
	}
}
