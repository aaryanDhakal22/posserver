// Package menu is the per-location catalog domain: menus → categories → items,
// plus reusable modifier groups (attached to items many-to-many). It is
// decoupled from the live order pipeline — a manager-editable source of truth.
// Prices are int64 cents (mirrors order.Money); tax is basis points (600 = 6.00%).
//
// The structs carry json tags so handlers + the read API marshal them directly.
package menu

import (
	"time"

	"github.com/google/uuid"
)

type Menu struct {
	ID          uuid.UUID `json:"id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	LocationID  uuid.UUID `json:"location_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Available   bool      `json:"available"`
	Position    int       `json:"position"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Category struct {
	ID               uuid.UUID  `json:"id"`
	TenantID         uuid.UUID  `json:"tenant_id"`
	LocationID       uuid.UUID  `json:"location_id"`
	MenuID           uuid.UUID  `json:"menu_id"`
	Name             string     `json:"name"`
	Description      string     `json:"description"`
	Available        bool       `json:"available"`
	Position         int        `json:"position"`
	DefaultPrinterID *uuid.UUID `json:"default_printer_id"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type Item struct {
	ID          uuid.UUID      `json:"id"`
	TenantID    uuid.UUID      `json:"tenant_id"`
	LocationID  uuid.UUID      `json:"location_id"`
	CategoryID  uuid.UUID      `json:"category_id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	PriceCents  int64          `json:"price_cents"`
	TaxRateBps  int            `json:"tax_rate_bps"`
	InStock     bool           `json:"in_stock"`
	OutUntil    *time.Time     `json:"out_until"`
	Tags        []string       `json:"tags"`
	PrinterID   *uuid.UUID     `json:"printer_id"`
	Position    int            `json:"position"`
	Attributes  map[string]any `json:"attributes"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type ModifierGroup struct {
	ID            uuid.UUID `json:"id"`
	TenantID      uuid.UUID `json:"tenant_id"`
	LocationID    uuid.UUID `json:"location_id"`
	Name          string    `json:"name"`
	Required      bool      `json:"required"`
	MinSelect     int       `json:"min_select"`
	MaxSelect     int       `json:"max_select"`
	AllowMultiple bool      `json:"allow_multiple"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type ModifierOption struct {
	ID              uuid.UUID `json:"id"`
	TenantID        uuid.UUID `json:"tenant_id"`
	LocationID      uuid.UUID `json:"location_id"`
	GroupID         uuid.UUID `json:"group_id"`
	Name            string    `json:"name"`
	PriceDeltaCents int64     `json:"price_delta_cents"`
	InStock         bool      `json:"in_stock"`
	Position        int       `json:"position"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ItemGroupLink is a modifier group attached to an item, with its attach order.
type ItemGroupLink struct {
	Group          ModifierGroup `json:"group"`
	AttachPosition int           `json:"attach_position"`
}

// --- Read-API aggregate (GetFullMenu) --------------------------------------

type FullMenu struct {
	LocationID uuid.UUID  `json:"location_id"`
	Menus      []MenuTree `json:"menus"`
}

type MenuTree struct {
	Menu
	Categories []CategoryTree `json:"categories"`
}

type CategoryTree struct {
	Category
	Items []ItemTree `json:"items"`
}

type ItemTree struct {
	Item
	ModifierGroups []GroupTree `json:"modifier_groups"`
}

type GroupTree struct {
	ModifierGroup
	Options []ModifierOption `json:"options"`
}
