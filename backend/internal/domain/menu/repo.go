package menu

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type CreateMenuParams struct {
	TenantID, LocationID uuid.UUID
	Name, Description    string
	Available            bool
	Position             int
}

type UpdateMenuParams struct {
	ID, TenantID      uuid.UUID
	Name, Description string
	Available         bool
}

type CreateCategoryParams struct {
	TenantID, LocationID, MenuID uuid.UUID
	Name, Description            string
	Available                    bool
	Position                     int
	DefaultPrinterID             *uuid.UUID
}

type UpdateCategoryParams struct {
	ID, TenantID      uuid.UUID
	Name, Description string
	Available         bool
	DefaultPrinterID  *uuid.UUID
}

type CreateItemParams struct {
	TenantID, LocationID, CategoryID uuid.UUID
	Name, Description                string
	PriceCents                       int64
	TaxRateBps                       int
	InStock                          bool
	Tags                             []string
	Position                         int
}

type UpdateItemParams struct {
	ID, TenantID      uuid.UUID
	Name, Description string
	PriceCents        int64
	TaxRateBps        int
	InStock           bool
	OutUntil          *time.Time
	Tags              []string
	Attributes        map[string]any
}

type CreateGroupParams struct {
	TenantID, LocationID uuid.UUID
	Name                 string
	Required             bool
	MinSelect, MaxSelect int
	AllowMultiple        bool
}

type UpdateGroupParams struct {
	ID, TenantID         uuid.UUID
	Name                 string
	Required             bool
	MinSelect, MaxSelect int
	AllowMultiple        bool
}

type CreateOptionParams struct {
	TenantID, LocationID, GroupID uuid.UUID
	Name                          string
	PriceDeltaCents               int64
	InStock                       bool
	Position                      int
}

type UpdateOptionParams struct {
	ID, TenantID    uuid.UUID
	Name            string
	PriceDeltaCents int64
	InStock         bool
}

// RouteEntry is one row of a location's kitchen routing table: a menu item and
// its effective printer (the item's own, else its category default; nil = none).
// ItemNameCount is one vendor item name as ordered at a location.
type ItemNameCount struct {
	Name     string
	Orders   int64
	LastSeen time.Time
}

type RouteEntry struct {
	ItemID    uuid.UUID
	Name      string
	PrinterID *uuid.UUID
}

type Repository interface {
	// ListRoutingByLocation returns the location's menu items with their
	// effective printer, best candidate first (see query ListRoutingByLocation).
	ListRoutingByLocation(ctx context.Context, locationID uuid.UUID) ([]RouteEntry, error)

	// ListRecentItemNames returns the top-level item names ordered at the
	// location since `since`, with counts and last-seen times.
	ListRecentItemNames(ctx context.Context, tenantID, locationID uuid.UUID, since time.Time) ([]ItemNameCount, error)

	// Menus
	CreateMenu(ctx context.Context, p CreateMenuParams) (*Menu, error)
	GetMenu(ctx context.Context, id, tenantID uuid.UUID) (*Menu, error)
	ListMenusByLocation(ctx context.Context, locationID uuid.UUID) ([]Menu, error)
	UpdateMenu(ctx context.Context, p UpdateMenuParams) (*Menu, error)
	ReorderMenus(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) error
	DeleteMenu(ctx context.Context, id, tenantID uuid.UUID) error

	// Categories
	CreateCategory(ctx context.Context, p CreateCategoryParams) (*Category, error)
	GetCategory(ctx context.Context, id, tenantID uuid.UUID) (*Category, error)
	ListCategoriesByMenu(ctx context.Context, menuID uuid.UUID) ([]Category, error)
	UpdateCategory(ctx context.Context, p UpdateCategoryParams) (*Category, error)
	ReorderCategories(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) error
	DeleteCategory(ctx context.Context, id, tenantID uuid.UUID) error

	// Items
	CreateItem(ctx context.Context, p CreateItemParams) (*Item, error)
	GetItem(ctx context.Context, id, tenantID uuid.UUID) (*Item, error)
	ListItemsByCategory(ctx context.Context, categoryID uuid.UUID) ([]Item, error)
	UpdateItem(ctx context.Context, p UpdateItemParams) (*Item, error)
	SetItemPrinter(ctx context.Context, id, tenantID uuid.UUID, printerID *uuid.UUID) error
	ReorderItems(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) error
	DeleteItem(ctx context.Context, id, tenantID uuid.UUID) error

	// Modifier groups (reusable)
	CreateGroup(ctx context.Context, p CreateGroupParams) (*ModifierGroup, error)
	GetGroup(ctx context.Context, id, tenantID uuid.UUID) (*ModifierGroup, error)
	ListGroupsByLocation(ctx context.Context, locationID uuid.UUID) ([]ModifierGroup, error)
	UpdateGroup(ctx context.Context, p UpdateGroupParams) (*ModifierGroup, error)
	DeleteGroup(ctx context.Context, id, tenantID uuid.UUID) error // detach-then-delete (tx)
	CountItemsForGroup(ctx context.Context, id, tenantID uuid.UUID) (int, error)

	// Options
	CreateOption(ctx context.Context, p CreateOptionParams) (*ModifierOption, error)
	ListOptionsByGroup(ctx context.Context, groupID uuid.UUID) ([]ModifierOption, error)
	UpdateOption(ctx context.Context, p UpdateOptionParams) (*ModifierOption, error)
	ReorderOptions(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) error
	DeleteOption(ctx context.Context, id, tenantID uuid.UUID) error

	// Item ↔ group (M:N)
	AttachGroup(ctx context.Context, tenantID, itemID, groupID uuid.UUID, position int) error
	DetachGroup(ctx context.Context, tenantID, itemID, groupID uuid.UUID) error
	ListGroupsForItem(ctx context.Context, itemID, tenantID uuid.UUID) ([]ItemGroupLink, error)

	// Read API
	GetFullMenu(ctx context.Context, locationID uuid.UUID) (*FullMenu, error)
}
