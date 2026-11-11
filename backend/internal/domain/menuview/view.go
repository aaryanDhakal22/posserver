// Package menuview models the views of a location's menu bank (main 3.0): each
// kind of sales channel — web, call center (operator), counter terminal,
// vendor feed — reads its own view of the same bank. A view renames,
// describes, adds photos, hides, regroups, reprices (a price rule plus
// per-entry overrides), merges and augments, and every view item stays
// anchored to bank ids, so an order placed through any view maps back to the
// bank (kitchen tickets print the bank's kitchen names; the customer sees the
// view's). A vendor view instead maps the vendor's item names to bank items.
//
// Compile is pure: bank + view + entries → Build.
package menuview

import (
	"time"

	"quiccpos/main/internal/domain/menu"

	"github.com/google/uuid"
)

// Kind is the kind of channel a view is for (same values as channel.Kind).
type Kind string

const (
	KindWeb      Kind = "web"
	KindOperator Kind = "operator"
	KindCounter  Kind = "counter"
	KindVendor   Kind = "vendor"
)

// Kinds lists every view kind.
var Kinds = []Kind{KindWeb, KindOperator, KindCounter, KindVendor}

func (k Kind) Valid() bool {
	for _, x := range Kinds {
		if x == k {
			return true
		}
	}
	return false
}

// Rounding modes of a price rule.
const (
	RoundNone   = "none"
	RoundCeil99 = "ceil_99" // up to the next X.99 (a $14.39 marked-up price shows $14.99)
)

// Rules are a view's defaults.
type Rules struct {
	// MarkupBps raises every bank price by this many basis points (2000 =
	// +20%, a marketplace that charges the store 20%). 0 = bank prices.
	MarkupBps int `json:"markup_bps"`
	// Round rounds a marked-up price (RoundNone | RoundCeil99). $0 stays $0.
	Round string `json:"round"`
	// HiddenCategories are bank category ids this view leaves out.
	HiddenCategories []uuid.UUID `json:"hidden_categories"`
	// Menus are the bank menus this view shows (empty = every menu): the
	// counter view shows only the Counter menu (TERMINAL-PLAN 0d).
	Menus []uuid.UUID `json:"menus,omitempty"`
	// RequirePhoto puts web items without a photo on the needs-attention
	// list (they still show).
	RequirePhoto bool `json:"require_photo"`
}

// View is one view of a location's bank.
type View struct {
	ID         uuid.UUID `json:"id"`
	TenantID   uuid.UUID `json:"tenant_id"`
	LocationID uuid.UUID `json:"location_id"`
	Kind       Kind      `json:"kind"`
	Name       string    `json:"name"`
	Rules      Rules     `json:"rules"`
	// HasLayout: a counter view with a counter layout (GET …/views/:id/layout).
	HasLayout bool      `json:"has_layout"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Entry kinds.
const (
	EntryItem    = "item"    // change one bank item (name, text, photo, hide, move, price)
	EntryMerge   = "merge"   // one view item = a choice between several bank items
	EntryAugment = "augment" // an extra view item made of bank items, at its own price
	EntryMap     = "map"     // vendor views: a vendor item name [+ size] → a bank item [+ size]
)

// Anchor ties an entry to the bank.
type Anchor struct {
	ItemIDs []uuid.UUID `json:"item_ids"`
	SizeID  *uuid.UUID  `json:"size_id,omitempty"`
}

// Entry is one change a view makes to the bank.
type Entry struct {
	ID          uuid.UUID `json:"id"`
	ViewID      uuid.UUID `json:"view_id"`
	Kind        string    `json:"kind"`
	Anchor      Anchor    `json:"anchor"`
	DisplayName string    `json:"display_name"`
	Description string    `json:"description"`
	Photo       string    `json:"photo"`
	// Category moves the item into this view category (by name; created
	// when the bank has none of that name). "" = its bank category.
	Category string `json:"category"`
	Position int    `json:"position"`
	Hidden   bool   `json:"hidden"`
	// PriceOverrideCents is the final price (no rule applied) of an unsized
	// item, a merge's base or an augment.
	PriceOverrideCents *int64 `json:"price_override_cents"`
	// Extra holds kind-specific data: size_prices {size_id: cents} for an
	// item entry, labels {item_id: label} for a merge, vendor_name +
	// vendor_size for a map.
	Extra     Extra     `json:"extra"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Extra is an entry's kind-specific data.
type Extra struct {
	SizePrices map[string]int64  `json:"size_prices,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	VendorName string            `json:"vendor_name,omitempty"`
	VendorSize string            `json:"vendor_size,omitempty"`
}

// CounterOnlyMenus are the bank menus only a counter terminal sells: a
// counter view's Menus rule names the menu, and no web or operator view
// shows it (its own Menus rule leaves it out, or every one of the menu's
// categories is in its HiddenCategories). Online orders never route to their
// items (menuApp.RouteItems). Vendor views are maps, not menus: they don't
// count.
func CounterOnlyMenus(bank *menu.Bank, views []View) map[uuid.UUID]bool {
	out := map[uuid.UUID]bool{}
	for _, v := range views {
		if v.Kind == KindCounter {
			for _, id := range v.Rules.Menus {
				out[id] = true
			}
		}
	}
	if len(out) == 0 {
		return out
	}
	for _, m := range bank.Menus {
		if !out[m.ID] {
			continue
		}
		for _, v := range views {
			if (v.Kind == KindWeb || v.Kind == KindOperator) && showsMenu(v.Rules, m) {
				delete(out, m.ID)
				break
			}
		}
	}
	// A named menu that's gone from the bank stays out: it has no items.
	return out
}

// showsMenu: does a view with these rules show any of the menu's categories?
func showsMenu(r Rules, m menu.BankMenu) bool {
	if len(r.Menus) > 0 {
		in := false
		for _, id := range r.Menus {
			in = in || id == m.ID
		}
		if !in {
			return false
		}
	}
	hidden := map[uuid.UUID]bool{}
	for _, id := range r.HiddenCategories {
		hidden[id] = true
	}
	for _, c := range m.Categories {
		if !hidden[c.ID] {
			return true
		}
	}
	return len(m.Categories) == 0
}
