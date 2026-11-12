// Package bankdoc is the bank document: a location's whole Canonical Menu
// bank, its views and its deals as one JSON document that names things
// instead of using ids, so it can be written by a tool (the weborder menu
// converter), reviewed, applied in one transaction and exported again.
//
// Matching rules (the import):
//   - sizes, modifier groups, menus, categories and items match the existing
//     rows of the location by name (case, repeated spaces and outer spaces
//     ignored); an option matches by name within its group; a view by kind +
//     name; a deal by name. Category and item names must be unique in the
//     document (they are what views and deals refer to).
//   - Absent optional fields keep what is there: `available` / `in_stock`
//     (new rows: true), `printer` (new rows: none), `no_kitchen_ticket`
//     (new rows: false), an item's `tags`,
//     `sizes` and `groups`. Sold-out state (`out_until`) is never touched.
//   - A view's entries are replaced by the document's (views are generated).
//   - Nothing is deleted unless the import is asked to prune; views are never
//     deleted (channels point at them).
package bankdoc

import (
	"context"
	"errors"
	"strings"

	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/menuview"
	"quiccpos/main/internal/domain/pricing"

	"github.com/google/uuid"
)

// Format is the document format this main reads and writes.
const Format = 1

// ErrRollback, returned from inside Tx work, rolls the transaction back
// without failing (a dry run).
var ErrRollback = errors.New("roll back")

// Document is a location's bank, views and deals.
type Document struct {
	Format int     `json:"format"`
	Sizes  []Size  `json:"sizes"`
	Groups []Group `json:"groups"`
	Menus  []Menu  `json:"menus"`
	Views  []View  `json:"views"`
	Deals  []Deal  `json:"deals"`
	// Families are the store's item / mod families (00045); items and
	// options name theirs, coupon parts name item families.
	Families []Family `json:"families,omitempty"`
}

// Family is a family by name: kind item | mod, a mod family's class
// regular | premium | unpriced (absent: regular).
type Family struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Class string `json:"class,omitempty"`
}

// Size is a store size ("12 Medium", short "12\"").
type Size struct {
	Name  string `json:"name"`
	Short string `json:"short"`
}

// Group is a reusable modifier group.
type Group struct {
	Name          string   `json:"name"`
	Required      bool     `json:"required"`
	MinSelect     int      `json:"min_select"`
	MaxSelect     int      `json:"max_select"` // 0 = unlimited
	AllowMultiple bool     `json:"allow_multiple"`
	Portions      bool     `json:"portions"` // halves
	Options       []Option `json:"options"`
}

// Option is one choice of a group. Prices maps size names to the option's
// price on an item of that size; PriceCents is the price on any other item.
type Option struct {
	Name        string           `json:"name"`
	PriceCents  int64            `json:"price_cents"`
	InStock     *bool            `json:"in_stock,omitempty"`
	IsDefault   bool             `json:"is_default"`
	KitchenName string           `json:"kitchen_name"`
	Prices      map[string]int64 `json:"prices,omitempty"`
	// Family: a mod family's name (absent keeps, "" = none).
	Family *string `json:"family,omitempty"`
}

// Menu → Category → Item is the bank's tree.
type Menu struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Available   *bool      `json:"available,omitempty"`
	Categories  []Category `json:"categories"`
}

// Category: Printer names the store printer (nil = keep, "" = none).
// NoKitchenTicket: its items without a printer of their own get no station
// ticket (receipt only; nil = keep, new rows false).
type Category struct {
	Name            string  `json:"name"`
	Description     string  `json:"description"`
	Available       *bool   `json:"available,omitempty"`
	Printer         *string `json:"printer,omitempty"`
	NoKitchenTicket *bool   `json:"no_kitchen_ticket,omitempty"`
	Items           []Item  `json:"items"`
}

// Item is one bank item. Printer nil = keep ("" = inherit the category's);
// Sizes / Groups / Tags nil = keep, empty = none.
type Item struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	PriceCents  int64       `json:"price_cents"`
	TaxRateBps  int         `json:"tax_rate_bps"`
	InStock     *bool       `json:"in_stock,omitempty"`
	Tags        *[]string   `json:"tags,omitempty"`
	Printer     *string     `json:"printer,omitempty"`
	KitchenName string      `json:"kitchen_name"`
	SKU         string      `json:"sku"`
	Sizes       *[]ItemSize `json:"sizes,omitempty"`
	Groups      *[]string   `json:"groups,omitempty"`
	// HalfBase names a specialty pizza's plain pizza (another item), for
	// half-and-half pricing (the item's half_base_item attribute). nil =
	// keep, "" = none.
	HalfBase *string `json:"half_base,omitempty"`
	// NoTax: the item isn't taxed; OpenPrice: the cashier types its price
	// (the counter). Item attributes no_tax / open_price; nil = keep.
	NoTax     *bool `json:"no_tax,omitempty"`
	OpenPrice *bool `json:"open_price,omitempty"`
	// NoKitchenTicket: no station ticket of its own and not in "Also on
	// this order" (receipt only; wins over Printer). nil = keep, new rows
	// false.
	NoKitchenTicket *bool `json:"no_kitchen_ticket,omitempty"`
	// Family: an item family's name (absent keeps, "" = none).
	Family *string `json:"family,omitempty"`
}

// ItemSize is a size an item comes in.
type ItemSize struct {
	Size        string `json:"size"`
	PriceCents  int64  `json:"price_cents"`
	KitchenName string `json:"kitchen_name"`
}

// View is a view of the bank and its entries (replaced as a whole). Layout
// is a counter view's layout (nil = keep the one it has; clear it with
// DELETE …/views/:id/layout).
type View struct {
	Kind    string  `json:"kind"`
	Name    string  `json:"name"`
	Rules   Rules   `json:"rules"`
	Entries []Entry `json:"entries"`
	Layout  *Layout `json:"layout,omitempty"`
}

// Layout is a counter layout (menuview.Layout) by names: flows are keyed by
// item name, actions and steps name items, sizes, categories, groups,
// options and deals.
type Layout = menuview.LayoutOf[LayoutAction, LayoutStep]

// LayoutAction is a menuview.Action by names (Option is a name within Group).
type LayoutAction struct {
	Type     string `json:"type"`
	Item     string `json:"item,omitempty"`
	Size     string `json:"size,omitempty"`
	Category string `json:"category,omitempty"`
	Deal     string `json:"deal,omitempty"`
	Name     string `json:"name,omitempty"` // function
	Group    string `json:"group,omitempty"`
	Option   string `json:"option,omitempty"`
	State    string `json:"state,omitempty"`
	Mode     string `json:"mode,omitempty"` // option: add | no
}

// LayoutStep is a menuview.Step with a size name.
type LayoutStep struct {
	Screen        string `json:"screen"`
	Size          string `json:"size,omitempty"`
	RequiredCount int    `json:"required_count"`
	Forced        bool   `json:"forced"`
	Prompt        string `json:"prompt"`
}

// Rules are menuview.Rules with category names.
type Rules struct {
	MarkupBps        int      `json:"markup_bps"`
	Round            string   `json:"round"`
	HiddenCategories []string `json:"hidden_categories"`
	// Menus: the view shows only these menus (names; empty = every menu).
	Menus        []string `json:"menus,omitempty"`
	RequirePhoto bool     `json:"require_photo"`
}

// Entry is a menuview.Entry with item and size names.
type Entry struct {
	Kind               string            `json:"kind"`
	Items              []string          `json:"items"`
	Size               string            `json:"size,omitempty"`
	DisplayName        string            `json:"display_name,omitempty"`
	Description        string            `json:"description,omitempty"`
	Photo              string            `json:"photo,omitempty"`
	Category           string            `json:"category,omitempty"`
	Position           *int              `json:"position,omitempty"` // nil = its index
	Hidden             bool              `json:"hidden,omitempty"`
	PriceOverrideCents *int64            `json:"price_override_cents,omitempty"`
	SizePrices         map[string]int64  `json:"size_prices,omitempty"` // size name → cents
	Labels             map[string]string `json:"labels,omitempty"`      // item name → label
	VendorName         string            `json:"vendor_name,omitempty"`
	VendorSize         string            `json:"vendor_size,omitempty"`
}

// Deal is a pricing.StoreDeal whose eligibility (items, categories, sizes)
// names bank items, categories and sizes instead of ids.
type Deal = pricing.StoreDeal

// Norm is how names match: lower case, inner whitespace collapsed, trimmed.
func Norm(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// ---- the import's store side ---------------------------------------------------

// Printer is a store printer (name → id).
type Printer struct {
	ID   uuid.UUID
	Name string
}

// GroupRow is a modifier group with its bank flag.
type GroupRow struct {
	menu.ModifierGroup
	Portions bool
}

// OptionRow is a modifier option with its bank fields.
type OptionRow struct {
	menu.ModifierOption
	IsDefault   bool
	KitchenName string
}

// Tx is everything the import reads and writes, inside one transaction that
// holds the location's import lock. Rows are the location's; ids are checked
// against the tenant by the store.
type Tx interface {
	Bank(ctx context.Context) (*menu.Bank, error)
	Printers(ctx context.Context) ([]Printer, error)
	Views(ctx context.Context) ([]menuview.View, error)
	Entries(ctx context.Context, viewID uuid.UUID) ([]menuview.Entry, error)
	Deals(ctx context.Context) ([]pricing.StoreDeal, error)

	CreateSize(ctx context.Context, name, short string, position int) (uuid.UUID, error)
	UpdateSize(ctx context.Context, id uuid.UUID, name, short string, position int) error
	DeleteSize(ctx context.Context, id uuid.UUID) error

	CreateMenu(ctx context.Context, m menu.Menu) (uuid.UUID, error)
	UpdateMenu(ctx context.Context, m menu.Menu) error // name, description, available, position
	DeleteMenu(ctx context.Context, id uuid.UUID) error

	CreateCategory(ctx context.Context, c menu.Category) (uuid.UUID, error)
	UpdateCategory(ctx context.Context, c menu.Category) error // + menu (move), printer, position
	DeleteCategory(ctx context.Context, id uuid.UUID) error

	CreateItem(ctx context.Context, it menu.Item) (uuid.UUID, error)
	UpdateItem(ctx context.Context, it menu.Item) error // + category (move), printer, position
	SetItemBank(ctx context.Context, id uuid.UUID, x menu.ItemExtra) error
	SetItemGroups(ctx context.Context, id uuid.UUID, groups []uuid.UUID) error
	DeleteItem(ctx context.Context, id uuid.UUID) error

	CreateGroup(ctx context.Context, g GroupRow) (uuid.UUID, error)
	UpdateGroup(ctx context.Context, g GroupRow) error
	DeleteGroup(ctx context.Context, id uuid.UUID) error
	CreateOption(ctx context.Context, o OptionRow) (uuid.UUID, error)
	UpdateOption(ctx context.Context, o OptionRow) error // + position, default, kitchen name
	SetOptionPrices(ctx context.Context, id uuid.UUID, prices []menu.OptionPrice) error
	DeleteOption(ctx context.Context, id uuid.UUID) error

	CreateView(ctx context.Context, kind menuview.Kind, name string, rules menuview.Rules) (uuid.UUID, error)
	UpdateView(ctx context.Context, id uuid.UUID, name string, rules menuview.Rules) error
	ReplaceEntries(ctx context.Context, view menuview.View, entries []menuview.Entry) error
	Layout(ctx context.Context, viewID uuid.UUID) (*menuview.Layout, error)
	SetLayout(ctx context.Context, viewID uuid.UUID, l *menuview.Layout) error

	CreateFamily(ctx context.Context, f menu.Family) (uuid.UUID, error)
	UpdateFamily(ctx context.Context, f menu.Family) error // name, class
	DeleteFamily(ctx context.Context, id uuid.UUID) error
	SetFamily(ctx context.Context, option bool, ids []uuid.UUID, family *uuid.UUID) error // items, or options

	CreateDeal(ctx context.Context, d pricing.StoreDeal) error
	UpdateDeal(ctx context.Context, id uuid.UUID, d pricing.StoreDeal) error
	DeleteDeal(ctx context.Context, id uuid.UUID) error
}
