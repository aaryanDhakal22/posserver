package menu

import (
	"time"

	"github.com/google/uuid"
)

// The bank (main 3.0): today's menu tables are the Canonical Menu bank — the
// strict, finest-grained, priced menu of a location — and these are what the
// bank adds to them. Every sales channel reads a view of it (menuview).

// Size is one of a location's store-wide sizes ("12 Medium").
type Size struct {
	ID         uuid.UUID `json:"id"`
	LocationID uuid.UUID `json:"location_id"`
	Name       string    `json:"name"`
	Short      string    `json:"short"`
	Position   int       `json:"position"`
}

// ItemSize is a size an item comes in and its price.
type ItemSize struct {
	SizeID      uuid.UUID `json:"size_id"`
	PriceCents  int64     `json:"price_cents"`
	KitchenName string    `json:"kitchen_name"`
	Position    int       `json:"position"`
}

// OptionPrice is a modifier option's price on an item of one size.
type OptionPrice struct {
	SizeID     uuid.UUID `json:"size_id"`
	PriceCents int64     `json:"price_cents"`
}

// ItemExtra is the bank data of an item beyond the legacy Item.
type ItemExtra struct {
	KitchenName string     `json:"kitchen_name"`
	SKU         string     `json:"sku"`
	Sizes       []ItemSize `json:"sizes"`
}

// OptionExtra is the bank data of a modifier option beyond the legacy one.
type OptionExtra struct {
	OptionID    uuid.UUID     `json:"option_id"`
	IsDefault   bool          `json:"is_default"`
	KitchenName string        `json:"kitchen_name"`
	Prices      []OptionPrice `json:"prices"`
}

// GroupExtra is the bank data of a modifier group beyond the legacy one.
type GroupExtra struct {
	Portions bool          `json:"portions"`
	Options  []OptionExtra `json:"options"`
}

// Bank is a location's whole bank, as views compile from it.
type Bank struct {
	LocationID uuid.UUID               `json:"location_id"`
	Version    int64                   `json:"version"`
	Sizes      []Size                  `json:"sizes"`
	Menus      []BankMenu              `json:"menus"`
	Groups     map[uuid.UUID]BankGroup `json:"groups"`
	Families   []Family                `json:"families"`
}

// BankMenu / BankCategory / BankItem are the bank's tree.
type BankMenu struct {
	Menu
	Categories []BankCategory `json:"categories"`
}

type BankCategory struct {
	Category
	Items []BankItem `json:"items"`
}

type BankItem struct {
	Item
	KitchenName string      `json:"kitchen_name"`
	SKU         string      `json:"sku"`
	Sizes       []ItemSize  `json:"sizes"`
	GroupIDs    []uuid.UUID `json:"group_ids"` // attached modifier groups, in attach order
	FamilyID    *uuid.UUID  `json:"family_id"` // an item family (nil = no family yet)
}

// BankGroup is a modifier group with its options and their bank data.
type BankGroup struct {
	ModifierGroup
	Portions bool         `json:"portions"`
	Options  []BankOption `json:"options"`
}

type BankOption struct {
	ModifierOption
	IsDefault   bool          `json:"is_default"`
	KitchenName string        `json:"kitchen_name"`
	Prices      []OptionPrice `json:"prices"`
	FamilyID    *uuid.UUID    `json:"family_id"` // a mod family (nil = no family yet)
}

// FamilyByID finds a store family.
func (b *Bank) FamilyByID(id uuid.UUID) (Family, bool) {
	for _, f := range b.Families {
		if f.ID == id {
			return f, true
		}
	}
	return Family{}, false
}

// Item finds a bank item by id.
func (b *Bank) Item(id uuid.UUID) (*BankItem, *BankCategory, bool) {
	for mi := range b.Menus {
		for ci := range b.Menus[mi].Categories {
			c := &b.Menus[mi].Categories[ci]
			for ii := range c.Items {
				if c.Items[ii].ID == id {
					return &c.Items[ii], c, true
				}
			}
		}
	}
	return nil, nil, false
}

// SizeByID finds a store size.
func (b *Bank) SizeByID(id uuid.UUID) (Size, bool) {
	for _, s := range b.Sizes {
		if s.ID == id {
			return s, true
		}
	}
	return Size{}, false
}

// Version is a location's menu version (bumped by every bank or view change).
type Version struct {
	Version   int64     `json:"version"`
	ChangedAt time.Time `json:"changed_at"`
}
