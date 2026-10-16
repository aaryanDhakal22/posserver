package repositories

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

var _ menu.Repository = (*MenuRepository)(nil)

type MenuRepository struct {
	pool   *pgxpool.Pool
	logger zerolog.Logger
}

func NewMenuRepository(pool *pgxpool.Pool, lg zerolog.Logger) *MenuRepository {
	return &MenuRepository{pool: pool, logger: lg.With().Str("module", "menu-repo").Logger()}
}

// --- conversions -----------------------------------------------------------

func pgID(u uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: u, Valid: true} }
func pgIDPtr(u *uuid.UUID) pgtype.UUID {
	if u == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *u, Valid: true}
}
func idOf(p pgtype.UUID) uuid.UUID { return uuid.UUID(p.Bytes) }
func idPtrOf(p pgtype.UUID) *uuid.UUID {
	if !p.Valid {
		return nil
	}
	u := uuid.UUID(p.Bytes)
	return &u
}
func i32(i int) int32 { return int32(i) }
func tagsToJSON(tags []string) []byte {
	if tags == nil {
		tags = []string{}
	}
	b, _ := json.Marshal(tags)
	return b
}
func tagsFromJSON(raw []byte) []string {
	if len(raw) == 0 {
		return []string{}
	}
	var out []string
	if json.Unmarshal(raw, &out) != nil {
		return []string{}
	}
	return out
}
func attrsToJSON(m map[string]any) []byte {
	if m == nil {
		return []byte("{}")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return b
}
func attrsFromJSON(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return map[string]any{}
	}
	return out
}

func toDomainMenu(r models.Menu) menu.Menu {
	return menu.Menu{
		ID: idOf(r.ID), TenantID: idOf(r.TenantID), LocationID: idOf(r.LocationID),
		Name: r.Name, Description: r.Description, Available: r.Available, Position: int(r.Position),
		CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
}

func toDomainCategory(r models.MenuCategory) menu.Category {
	return menu.Category{
		ID: idOf(r.ID), TenantID: idOf(r.TenantID), LocationID: idOf(r.LocationID), MenuID: idOf(r.MenuID),
		Name: r.Name, Description: r.Description, Available: r.Available, Position: int(r.Position),
		DefaultPrinterID: idPtrOf(r.DefaultPrinterID),
		CreatedAt:        r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
}

func toDomainItem(r models.MenuItem) menu.Item {
	it := menu.Item{
		ID: idOf(r.ID), TenantID: idOf(r.TenantID), LocationID: idOf(r.LocationID), CategoryID: idOf(r.CategoryID),
		Name: r.Name, Description: r.Description, PriceCents: r.PriceCents, TaxRateBps: int(r.TaxRateBps),
		InStock: r.InStock, Tags: tagsFromJSON(r.Tags), PrinterID: idPtrOf(r.PrinterID),
		Position: int(r.Position), Attributes: attrsFromJSON(r.Attributes),
		CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
	if r.OutUntil.Valid {
		t := r.OutUntil.Time
		it.OutUntil = &t
	}
	return it
}

func toDomainGroup(r models.ModifierGroup) menu.ModifierGroup {
	return menu.ModifierGroup{
		ID: idOf(r.ID), TenantID: idOf(r.TenantID), LocationID: idOf(r.LocationID),
		Name: r.Name, Required: r.Required, MinSelect: int(r.MinSelect), MaxSelect: int(r.MaxSelect),
		AllowMultiple: r.AllowMultiple, CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
}

func toDomainOption(r models.ModifierOption) menu.ModifierOption {
	return menu.ModifierOption{
		ID: idOf(r.ID), TenantID: idOf(r.TenantID), LocationID: idOf(r.LocationID), GroupID: idOf(r.GroupID),
		Name: r.Name, PriceDeltaCents: r.PriceDeltaCents, InStock: r.InStock, Position: int(r.Position),
		CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
	}
}

// --- Menus -----------------------------------------------------------------

func (r *MenuRepository) CreateMenu(ctx context.Context, p menu.CreateMenuParams) (*menu.Menu, error) {
	row, err := models.New(r.pool).CreateMenu(ctx, models.CreateMenuParams{
		TenantID: pgID(p.TenantID), LocationID: pgID(p.LocationID), Name: p.Name,
		Description: p.Description, Available: p.Available, Position: i32(p.Position),
	})
	if err != nil {
		return nil, fmt.Errorf("create menu: %w", err)
	}
	m := toDomainMenu(row)
	return &m, nil
}

func (r *MenuRepository) GetMenu(ctx context.Context, id, tenantID uuid.UUID) (*menu.Menu, error) {
	row, err := models.New(r.pool).GetMenuByID(ctx, models.GetMenuByIDParams{ID: pgID(id), TenantID: pgID(tenantID)})
	if err != nil {
		return nil, err
	}
	m := toDomainMenu(row)
	return &m, nil
}

func (r *MenuRepository) ListMenusByLocation(ctx context.Context, locationID uuid.UUID) ([]menu.Menu, error) {
	rows, err := models.New(r.pool).ListMenusByLocation(ctx, pgID(locationID))
	if err != nil {
		return nil, fmt.Errorf("list menus: %w", err)
	}
	out := make([]menu.Menu, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomainMenu(row))
	}
	return out, nil
}

func (r *MenuRepository) UpdateMenu(ctx context.Context, p menu.UpdateMenuParams) (*menu.Menu, error) {
	row, err := models.New(r.pool).UpdateMenu(ctx, models.UpdateMenuParams{
		ID: pgID(p.ID), TenantID: pgID(p.TenantID), Name: p.Name, Description: p.Description, Available: p.Available,
	})
	if err != nil {
		return nil, fmt.Errorf("update menu: %w", err)
	}
	m := toDomainMenu(row)
	return &m, nil
}

func (r *MenuRepository) ReorderMenus(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) error {
	return r.reorder(ctx, tenantID, ids, func(q *models.Queries, id pgtype.UUID, tid pgtype.UUID, pos int32) error {
		return q.SetMenuPosition(ctx, models.SetMenuPositionParams{ID: id, TenantID: tid, Position: pos})
	})
}

func (r *MenuRepository) DeleteMenu(ctx context.Context, id, tenantID uuid.UUID) error {
	return models.New(r.pool).DeleteMenu(ctx, models.DeleteMenuParams{ID: pgID(id), TenantID: pgID(tenantID)})
}

// --- Categories ------------------------------------------------------------

func (r *MenuRepository) CreateCategory(ctx context.Context, p menu.CreateCategoryParams) (*menu.Category, error) {
	row, err := models.New(r.pool).CreateMenuCategory(ctx, models.CreateMenuCategoryParams{
		TenantID: pgID(p.TenantID), LocationID: pgID(p.LocationID), MenuID: pgID(p.MenuID),
		Name: p.Name, Description: p.Description, Available: p.Available, Position: i32(p.Position),
		DefaultPrinterID: pgIDPtr(p.DefaultPrinterID),
	})
	if err != nil {
		return nil, fmt.Errorf("create category: %w", err)
	}
	c := toDomainCategory(row)
	return &c, nil
}

func (r *MenuRepository) GetCategory(ctx context.Context, id, tenantID uuid.UUID) (*menu.Category, error) {
	row, err := models.New(r.pool).GetMenuCategoryByID(ctx, models.GetMenuCategoryByIDParams{ID: pgID(id), TenantID: pgID(tenantID)})
	if err != nil {
		return nil, err
	}
	c := toDomainCategory(row)
	return &c, nil
}

func (r *MenuRepository) ListCategoriesByMenu(ctx context.Context, menuID uuid.UUID) ([]menu.Category, error) {
	rows, err := models.New(r.pool).ListCategoriesByMenu(ctx, pgID(menuID))
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	out := make([]menu.Category, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomainCategory(row))
	}
	return out, nil
}

func (r *MenuRepository) UpdateCategory(ctx context.Context, p menu.UpdateCategoryParams) (*menu.Category, error) {
	row, err := models.New(r.pool).UpdateMenuCategory(ctx, models.UpdateMenuCategoryParams{
		ID: pgID(p.ID), TenantID: pgID(p.TenantID), Name: p.Name, Description: p.Description,
		Available: p.Available, DefaultPrinterID: pgIDPtr(p.DefaultPrinterID),
	})
	if err != nil {
		return nil, fmt.Errorf("update category: %w", err)
	}
	c := toDomainCategory(row)
	return &c, nil
}

func (r *MenuRepository) ReorderCategories(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) error {
	return r.reorder(ctx, tenantID, ids, func(q *models.Queries, id pgtype.UUID, tid pgtype.UUID, pos int32) error {
		return q.SetCategoryPosition(ctx, models.SetCategoryPositionParams{ID: id, TenantID: tid, Position: pos})
	})
}

func (r *MenuRepository) DeleteCategory(ctx context.Context, id, tenantID uuid.UUID) error {
	return models.New(r.pool).DeleteMenuCategory(ctx, models.DeleteMenuCategoryParams{ID: pgID(id), TenantID: pgID(tenantID)})
}

// --- Items -----------------------------------------------------------------

func (r *MenuRepository) CreateItem(ctx context.Context, p menu.CreateItemParams) (*menu.Item, error) {
	row, err := models.New(r.pool).CreateMenuItem(ctx, models.CreateMenuItemParams{
		TenantID: pgID(p.TenantID), LocationID: pgID(p.LocationID), CategoryID: pgID(p.CategoryID),
		Name: p.Name, Description: p.Description, PriceCents: p.PriceCents, TaxRateBps: i32(p.TaxRateBps),
		InStock: p.InStock, OutUntil: pgtype.Timestamptz{}, Tags: tagsToJSON(p.Tags),
		PrinterID: pgtype.UUID{}, Position: i32(p.Position), Attributes: []byte("{}"),
	})
	if err != nil {
		return nil, fmt.Errorf("create item: %w", err)
	}
	it := toDomainItem(row)
	return &it, nil
}

func (r *MenuRepository) GetItem(ctx context.Context, id, tenantID uuid.UUID) (*menu.Item, error) {
	row, err := models.New(r.pool).GetMenuItemByID(ctx, models.GetMenuItemByIDParams{ID: pgID(id), TenantID: pgID(tenantID)})
	if err != nil {
		return nil, err
	}
	it := toDomainItem(row)
	return &it, nil
}

func (r *MenuRepository) ListItemsByCategory(ctx context.Context, categoryID uuid.UUID) ([]menu.Item, error) {
	rows, err := models.New(r.pool).ListItemsByCategory(ctx, pgID(categoryID))
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	out := make([]menu.Item, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomainItem(row))
	}
	return out, nil
}

func (r *MenuRepository) UpdateItem(ctx context.Context, p menu.UpdateItemParams) (*menu.Item, error) {
	ou := pgtype.Timestamptz{}
	if p.OutUntil != nil {
		ou = pgtype.Timestamptz{Time: *p.OutUntil, Valid: true}
	}
	row, err := models.New(r.pool).UpdateMenuItem(ctx, models.UpdateMenuItemParams{
		ID: pgID(p.ID), TenantID: pgID(p.TenantID), Name: p.Name, Description: p.Description,
		PriceCents: p.PriceCents, TaxRateBps: i32(p.TaxRateBps), InStock: p.InStock,
		OutUntil: ou, Tags: tagsToJSON(p.Tags), Attributes: attrsToJSON(p.Attributes),
	})
	if err != nil {
		return nil, fmt.Errorf("update item: %w", err)
	}
	it := toDomainItem(row)
	return &it, nil
}

func (r *MenuRepository) SetItemPrinter(ctx context.Context, id, tenantID uuid.UUID, printerID *uuid.UUID) error {
	return models.New(r.pool).SetItemPrinter(ctx, models.SetItemPrinterParams{ID: pgID(id), TenantID: pgID(tenantID), PrinterID: pgIDPtr(printerID)})
}

func (r *MenuRepository) ReorderItems(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) error {
	return r.reorder(ctx, tenantID, ids, func(q *models.Queries, id pgtype.UUID, tid pgtype.UUID, pos int32) error {
		return q.SetItemPosition(ctx, models.SetItemPositionParams{ID: id, TenantID: tid, Position: pos})
	})
}

func (r *MenuRepository) DeleteItem(ctx context.Context, id, tenantID uuid.UUID) error {
	return models.New(r.pool).DeleteMenuItem(ctx, models.DeleteMenuItemParams{ID: pgID(id), TenantID: pgID(tenantID)})
}

// --- Modifier groups -------------------------------------------------------

func (r *MenuRepository) CreateGroup(ctx context.Context, p menu.CreateGroupParams) (*menu.ModifierGroup, error) {
	row, err := models.New(r.pool).CreateModifierGroup(ctx, models.CreateModifierGroupParams{
		TenantID: pgID(p.TenantID), LocationID: pgID(p.LocationID), Name: p.Name, Required: p.Required,
		MinSelect: i32(p.MinSelect), MaxSelect: i32(p.MaxSelect), AllowMultiple: p.AllowMultiple,
	})
	if err != nil {
		return nil, fmt.Errorf("create group: %w", err)
	}
	g := toDomainGroup(row)
	return &g, nil
}

func (r *MenuRepository) GetGroup(ctx context.Context, id, tenantID uuid.UUID) (*menu.ModifierGroup, error) {
	row, err := models.New(r.pool).GetModifierGroupByID(ctx, models.GetModifierGroupByIDParams{ID: pgID(id), TenantID: pgID(tenantID)})
	if err != nil {
		return nil, err
	}
	g := toDomainGroup(row)
	return &g, nil
}

func (r *MenuRepository) ListGroupsByLocation(ctx context.Context, locationID uuid.UUID) ([]menu.ModifierGroup, error) {
	rows, err := models.New(r.pool).ListModifierGroupsByLocation(ctx, pgID(locationID))
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	out := make([]menu.ModifierGroup, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomainGroup(row))
	}
	return out, nil
}

func (r *MenuRepository) UpdateGroup(ctx context.Context, p menu.UpdateGroupParams) (*menu.ModifierGroup, error) {
	row, err := models.New(r.pool).UpdateModifierGroup(ctx, models.UpdateModifierGroupParams{
		ID: pgID(p.ID), TenantID: pgID(p.TenantID), Name: p.Name, Required: p.Required,
		MinSelect: i32(p.MinSelect), MaxSelect: i32(p.MaxSelect), AllowMultiple: p.AllowMultiple,
	})
	if err != nil {
		return nil, fmt.Errorf("update group: %w", err)
	}
	g := toDomainGroup(row)
	return &g, nil
}

// DeleteGroup detaches the group from all items, then deletes it (one tx).
func (r *MenuRepository) DeleteGroup(ctx context.Context, id, tenantID uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := models.New(tx)
	if err := q.DetachGroupFromAllItems(ctx, models.DetachGroupFromAllItemsParams{GroupID: pgID(id), TenantID: pgID(tenantID)}); err != nil {
		return fmt.Errorf("detach group: %w", err)
	}
	if err := q.DeleteModifierGroup(ctx, models.DeleteModifierGroupParams{ID: pgID(id), TenantID: pgID(tenantID)}); err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	return tx.Commit(ctx)
}

func (r *MenuRepository) CountItemsForGroup(ctx context.Context, id, tenantID uuid.UUID) (int, error) {
	n, err := models.New(r.pool).CountItemsForGroup(ctx, models.CountItemsForGroupParams{GroupID: pgID(id), TenantID: pgID(tenantID)})
	return int(n), err
}

// --- Options ---------------------------------------------------------------

func (r *MenuRepository) CreateOption(ctx context.Context, p menu.CreateOptionParams) (*menu.ModifierOption, error) {
	row, err := models.New(r.pool).CreateModifierOption(ctx, models.CreateModifierOptionParams{
		TenantID: pgID(p.TenantID), LocationID: pgID(p.LocationID), GroupID: pgID(p.GroupID),
		Name: p.Name, PriceDeltaCents: p.PriceDeltaCents, InStock: p.InStock, Position: i32(p.Position),
	})
	if err != nil {
		return nil, fmt.Errorf("create option: %w", err)
	}
	o := toDomainOption(row)
	return &o, nil
}

func (r *MenuRepository) ListOptionsByGroup(ctx context.Context, groupID uuid.UUID) ([]menu.ModifierOption, error) {
	rows, err := models.New(r.pool).ListOptionsByGroup(ctx, pgID(groupID))
	if err != nil {
		return nil, fmt.Errorf("list options: %w", err)
	}
	out := make([]menu.ModifierOption, 0, len(rows))
	for _, row := range rows {
		out = append(out, toDomainOption(row))
	}
	return out, nil
}

func (r *MenuRepository) UpdateOption(ctx context.Context, p menu.UpdateOptionParams) (*menu.ModifierOption, error) {
	row, err := models.New(r.pool).UpdateModifierOption(ctx, models.UpdateModifierOptionParams{
		ID: pgID(p.ID), TenantID: pgID(p.TenantID), Name: p.Name, PriceDeltaCents: p.PriceDeltaCents, InStock: p.InStock,
	})
	if err != nil {
		return nil, fmt.Errorf("update option: %w", err)
	}
	o := toDomainOption(row)
	return &o, nil
}

func (r *MenuRepository) ReorderOptions(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) error {
	return r.reorder(ctx, tenantID, ids, func(q *models.Queries, id pgtype.UUID, tid pgtype.UUID, pos int32) error {
		return q.SetOptionPosition(ctx, models.SetOptionPositionParams{ID: id, TenantID: tid, Position: pos})
	})
}

func (r *MenuRepository) DeleteOption(ctx context.Context, id, tenantID uuid.UUID) error {
	return models.New(r.pool).DeleteModifierOption(ctx, models.DeleteModifierOptionParams{ID: pgID(id), TenantID: pgID(tenantID)})
}

// --- Item ↔ group ----------------------------------------------------------

func (r *MenuRepository) AttachGroup(ctx context.Context, tenantID, itemID, groupID uuid.UUID, position int) error {
	return models.New(r.pool).AttachModifierGroup(ctx, models.AttachModifierGroupParams{
		TenantID: pgID(tenantID), ItemID: pgID(itemID), GroupID: pgID(groupID), Position: i32(position),
	})
}

func (r *MenuRepository) DetachGroup(ctx context.Context, tenantID, itemID, groupID uuid.UUID) error {
	return models.New(r.pool).DetachModifierGroup(ctx, models.DetachModifierGroupParams{
		TenantID: pgID(tenantID), ItemID: pgID(itemID), GroupID: pgID(groupID),
	})
}

func (r *MenuRepository) ListGroupsForItem(ctx context.Context, itemID, tenantID uuid.UUID) ([]menu.ItemGroupLink, error) {
	rows, err := models.New(r.pool).ListGroupsForItem(ctx, models.ListGroupsForItemParams{ItemID: pgID(itemID), TenantID: pgID(tenantID)})
	if err != nil {
		return nil, fmt.Errorf("list groups for item: %w", err)
	}
	out := make([]menu.ItemGroupLink, 0, len(rows))
	for _, row := range rows {
		out = append(out, menu.ItemGroupLink{
			Group: menu.ModifierGroup{
				ID: idOf(row.ID), TenantID: idOf(row.TenantID), LocationID: idOf(row.LocationID),
				Name: row.Name, Required: row.Required, MinSelect: int(row.MinSelect), MaxSelect: int(row.MaxSelect),
				AllowMultiple: row.AllowMultiple, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
			},
			AttachPosition: int(row.AttachPosition),
		})
	}
	return out, nil
}

// --- Full menu (read API) --------------------------------------------------

func (r *MenuRepository) GetFullMenu(ctx context.Context, locationID uuid.UUID) (*menu.FullMenu, error) {
	q := models.New(r.pool)
	lid := pgID(locationID)

	menuRows, err := q.ListMenusByLocation(ctx, lid)
	if err != nil {
		return nil, fmt.Errorf("menus: %w", err)
	}
	catRows, err := q.ListCategoriesByLocation(ctx, lid)
	if err != nil {
		return nil, fmt.Errorf("categories: %w", err)
	}
	itemRows, err := q.ListItemsByLocation(ctx, lid)
	if err != nil {
		return nil, fmt.Errorf("items: %w", err)
	}
	groupRows, err := q.ListModifierGroupsByLocation(ctx, lid)
	if err != nil {
		return nil, fmt.Errorf("groups: %w", err)
	}
	optRows, err := q.ListOptionsByLocation(ctx, lid)
	if err != nil {
		return nil, fmt.Errorf("options: %w", err)
	}
	linkRows, err := q.ListItemModifierGroupsByLocation(ctx, lid)
	if err != nil {
		return nil, fmt.Errorf("item links: %w", err)
	}

	// Index the leaves.
	optsByGroup := map[uuid.UUID][]menu.ModifierOption{}
	for _, o := range optRows {
		g := idOf(o.GroupID)
		optsByGroup[g] = append(optsByGroup[g], toDomainOption(o))
	}
	groupsByID := map[uuid.UUID]menu.ModifierGroup{}
	for _, g := range groupRows {
		groupsByID[idOf(g.ID)] = toDomainGroup(g)
	}
	groupIDsByItem := map[uuid.UUID][]uuid.UUID{}
	for _, l := range linkRows {
		it := idOf(l.ItemID)
		groupIDsByItem[it] = append(groupIDsByItem[it], idOf(l.GroupID))
	}
	itemsByCat := map[uuid.UUID][]menu.ItemTree{}
	for _, i := range itemRows {
		item := toDomainItem(i)
		gts := make([]menu.GroupTree, 0)
		for _, gid := range groupIDsByItem[item.ID] {
			if g, ok := groupsByID[gid]; ok {
				gts = append(gts, menu.GroupTree{ModifierGroup: g, Options: optsByGroup[gid]})
			}
		}
		itemsByCat[item.CategoryID] = append(itemsByCat[item.CategoryID], menu.ItemTree{Item: item, ModifierGroups: gts})
	}
	catsByMenu := map[uuid.UUID][]menu.CategoryTree{}
	for _, c := range catRows {
		cat := toDomainCategory(c)
		catsByMenu[cat.MenuID] = append(catsByMenu[cat.MenuID], menu.CategoryTree{Category: cat, Items: itemsByCat[cat.ID]})
	}

	out := &menu.FullMenu{LocationID: locationID, Menus: make([]menu.MenuTree, 0, len(menuRows))}
	for _, m := range menuRows {
		mm := toDomainMenu(m)
		out.Menus = append(out.Menus, menu.MenuTree{Menu: mm, Categories: catsByMenu[mm.ID]})
	}
	return out, nil
}

// --- reorder helper --------------------------------------------------------

func (r *MenuRepository) reorder(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID, set func(*models.Queries, pgtype.UUID, pgtype.UUID, int32) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := models.New(tx)
	tid := pgID(tenantID)
	for i, id := range ids {
		if err := set(q, pgID(id), tid, int32(i)); err != nil {
			return fmt.Errorf("set position: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ListRecentItemNames — see menu.Repository.
func (r *MenuRepository) ListRecentItemNames(ctx context.Context, tenantID, locationID uuid.UUID, since time.Time) ([]menu.ItemNameCount, error) {
	rows, err := models.New(r.pool).ListRecentItemNames(ctx, models.ListRecentItemNamesParams{
		TenantID:   pgtype.UUID{Bytes: tenantID, Valid: true},
		LocationID: pgtype.UUID{Bytes: locationID, Valid: true},
		ReceivedAt: pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("list recent item names: %w", err)
	}
	out := make([]menu.ItemNameCount, 0, len(rows))
	for _, row := range rows {
		out = append(out, menu.ItemNameCount{Name: row.Name, Orders: row.Orders, LastSeen: row.LastSeen.Time})
	}
	return out, nil
}

// ListRoutingByLocation — see menu.Repository.
func (r *MenuRepository) ListRoutingByLocation(ctx context.Context, locationID uuid.UUID) ([]menu.RouteEntry, error) {
	rows, err := models.New(r.pool).ListRoutingByLocation(ctx, pgtype.UUID{Bytes: locationID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list routing: %w", err)
	}
	out := make([]menu.RouteEntry, 0, len(rows))
	for _, row := range rows {
		e := menu.RouteEntry{ItemID: uuid.UUID(row.ItemID.Bytes), Name: row.ItemName}
		if row.PrinterID.Valid {
			pid := uuid.UUID(row.PrinterID.Bytes)
			e.PrinterID = &pid
		}
		out = append(out, e)
	}
	return out, nil
}
