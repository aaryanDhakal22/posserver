package menuview

import (
	"fmt"
	"strings"
)

// shortID is the first 8 characters of an id, for a line with no name.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// Describe says a needs-attention line in the manager's words, naming the
// thing by what it still has (a button's caption, an entry's name and
// changes, else a short id): "Button “Large Wings” on screen “Wings”: its
// item was deleted (item 1a2b3c4d)". The page's own wording is the same
// (ViewsTab); Message stays the short code-level text.
func (n NeedsAttention) Describe() string {
	q := func(s string) string { return "“" + s + "”" }
	switch {
	case n.Code == "anchor_gone":
		what := "Change"
		switch n.EntryKind {
		case EntryMerge:
			what = "Combined item"
		case EntryAugment:
			what = "Added item"
		}
		s := what
		if n.Name != "" {
			s += " " + q(n.Name)
		} else if what == "Change" {
			s = "A change"
		}
		if n.Change != "" {
			s += " (" + n.Change + ")"
		}
		lost := "its item was deleted"
		if n.EntryKind == EntryMerge || n.EntryKind == EntryAugment {
			lost = "one of its items was deleted"
		}
		return s + ": " + lost + " (item " + shortID(n.ItemID) + ")"
	case strings.HasPrefix(n.Code, "layout_"):
		var s string
		switch n.WhereKind {
		case "flow":
			if n.Name != "" {
				return "The counter flow of " + q(n.Name) + ": this view doesn't show the item"
			}
			return "The counter flow of a deleted item (item " + shortID(n.ItemID) + ")"
		case "tab", "screen":
			label := n.Caption
			if label == "" {
				label = n.Name
			}
			s = "Button"
			if label != "" {
				s += " " + q(label)
			}
			s += " on " + n.WhereKind + " " + q(n.Where)
			if n.Cell != nil {
				page := ""
				if n.Cell.PageName != "" {
					page = "page " + q(n.Cell.PageName) + ", "
				}
				s += fmt.Sprintf(" (%srow %d, column %d)", page, n.Cell.Row+1, n.Cell.Col+1)
			}
		default:
			s = "A counter button"
		}
		switch n.Code {
		case "layout_item_missing":
			if n.Name == "" {
				return s + ": its item was deleted (item " + shortID(n.ItemID) + ")"
			}
			return s + ": this view doesn't show " + q(n.Name)
		case "layout_size_missing":
			return s + ": its size no longer exists"
		case "layout_option_missing":
			return s + ": its option was deleted"
		case "layout_deal_missing":
			return s + ": its deal was deleted"
		}
		return s + ": " + n.Message
	}
	if n.Name != "" {
		return q(n.Name) + ": " + n.Message
	}
	return n.Message
}
