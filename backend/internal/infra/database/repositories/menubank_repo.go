package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/menuview"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MenuBankRepository persists what main 3.0 adds to the menu: the bank's
// sizes and per-size prices, the views of the bank and their compiled
// builds, and reads a location's whole bank + menu version.
type MenuBankRepository struct {
	pool *pgxpool.Pool
}

func NewMenuBankRepository(pool *pgxpool.Pool) *MenuBankRepository {
	return &MenuBankRepository{pool: pool}
}

// --- the whole bank ------------------------------------------------------------

// LoadBank reads a location's bank: menus → categories → items with their
// sizes and attached groups, every group with its options and their per-size
// prices, the store sizes and the menu version.
func (r *MenuBankRepository) LoadBank(ctx context.Context, locationID uuid.UUID) (*menu.Bank, error) {
	q := models.New(r.pool)
	lid := pgID(locationID)
	menus, err := q.ListMenusByLocation(ctx, lid)
	if err != nil {
		return nil, fmt.Errorf("menus: %w", err)
	}
	cats, err := q.ListCategoriesByLocation(ctx, lid)
	if err != nil {
		return nil, fmt.Errorf("categories: %w", err)
	}
	items, err := q.ListItemsByLocation(ctx, lid)
	if err != nil {
		return nil, fmt.Errorf("items: %w", err)
	}
	groups, err := q.ListModifierGroupsByLocation(ctx, lid)
	if err != nil {
		return nil, fmt.Errorf("groups: %w", err)
	}
	opts, err := q.ListOptionsByLocation(ctx, lid)
	if err != nil {
		return nil, fmt.Errorf("options: %w", err)
	}
	links, err := q.ListItemModifierGroupsByLocation(ctx, lid)
	if err != nil {
		return nil, fmt.Errorf("item links: %w", err)
	}
	sizes, err := q.ListMenuSizes(ctx, lid)
	if err != nil {
		return nil, fmt.Errorf("sizes: %w", err)
	}
	itemSizes, err := q.ListItemSizesByLocation(ctx, lid)
	if err != nil {
		return nil, fmt.Errorf("item sizes: %w", err)
	}
	optPrices, err := q.ListOptionPricesByLocation(ctx, lid)
	if err != nil {
		return nil, fmt.Errorf("option prices: %w", err)
	}
	ver, err := r.Version(ctx, locationID)
	if err != nil {
		return nil, err
	}

	b := &menu.Bank{LocationID: locationID, Version: ver.Version, Sizes: []menu.Size{}, Menus: []menu.BankMenu{},
		Groups: map[uuid.UUID]menu.BankGroup{}}
	for _, s := range sizes {
		b.Sizes = append(b.Sizes, sizeOf(s))
	}
	pricesByOpt := map[uuid.UUID][]menu.OptionPrice{}
	for _, p := range optPrices {
		pricesByOpt[idOf(p.OptionID)] = append(pricesByOpt[idOf(p.OptionID)], menu.OptionPrice{SizeID: idOf(p.SizeID), PriceCents: p.PriceCents})
	}
	optsByGroup := map[uuid.UUID][]menu.BankOption{}
	for _, o := range opts {
		g := idOf(o.GroupID)
		prices := pricesByOpt[idOf(o.ID)]
		if prices == nil {
			prices = []menu.OptionPrice{}
		}
		optsByGroup[g] = append(optsByGroup[g], menu.BankOption{ModifierOption: toDomainOption(o), IsDefault: o.IsDefault,
			KitchenName: o.KitchenName, Prices: prices})
	}
	for _, g := range groups {
		o := optsByGroup[idOf(g.ID)]
		if o == nil {
			o = []menu.BankOption{}
		}
		b.Groups[idOf(g.ID)] = menu.BankGroup{ModifierGroup: toDomainGroup(g), Portions: g.Portions, Options: o}
	}
	groupIDs := map[uuid.UUID][]uuid.UUID{}
	for _, l := range links {
		groupIDs[idOf(l.ItemID)] = append(groupIDs[idOf(l.ItemID)], idOf(l.GroupID))
	}
	sizesByItem := map[uuid.UUID][]menu.ItemSize{}
	for _, s := range itemSizes {
		sizesByItem[idOf(s.ItemID)] = append(sizesByItem[idOf(s.ItemID)], menu.ItemSize{SizeID: idOf(s.SizeID),
			PriceCents: s.PriceCents, KitchenName: s.KitchenName, Position: int(s.Position)})
	}
	itemsByCat := map[uuid.UUID][]menu.BankItem{}
	for _, it := range items {
		bi := menu.BankItem{Item: toDomainItem(it), KitchenName: it.KitchenName, SKU: it.Sku,
			Sizes: sizesByItem[idOf(it.ID)], GroupIDs: groupIDs[idOf(it.ID)]}
		if bi.Sizes == nil {
			bi.Sizes = []menu.ItemSize{}
		}
		if bi.GroupIDs == nil {
			bi.GroupIDs = []uuid.UUID{}
		}
		itemsByCat[bi.CategoryID] = append(itemsByCat[bi.CategoryID], bi)
	}
	catsByMenu := map[uuid.UUID][]menu.BankCategory{}
	for _, c := range cats {
		cat := toDomainCategory(c)
		its := itemsByCat[cat.ID]
		if its == nil {
			its = []menu.BankItem{}
		}
		catsByMenu[cat.MenuID] = append(catsByMenu[cat.MenuID], menu.BankCategory{Category: cat, Items: its})
	}
	for _, m := range menus {
		mm := toDomainMenu(m)
		cs := catsByMenu[mm.ID]
		if cs == nil {
			cs = []menu.BankCategory{}
		}
		b.Menus = append(b.Menus, menu.BankMenu{Menu: mm, Categories: cs})
	}
	return b, nil
}

// Version is a location's menu version (1 before anything ever changed).
func (r *MenuBankRepository) Version(ctx context.Context, locationID uuid.UUID) (menu.Version, error) {
	row, err := models.New(r.pool).GetMenuVersion(ctx, pgID(locationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return menu.Version{Version: 1}, nil
	}
	if err != nil {
		return menu.Version{}, fmt.Errorf("menu version: %w", err)
	}
	return menu.Version{Version: row.Version, ChangedAt: row.ChangedAt.Time}, nil
}

// --- sizes ---------------------------------------------------------------------

func sizeOf(s models.MenuSize) menu.Size {
	return menu.Size{ID: idOf(s.ID), LocationID: idOf(s.LocationID), Name: s.Name, Short: s.Short, Position: int(s.Position)}
}

func (r *MenuBankRepository) CreateSize(ctx context.Context, tenantID, locationID uuid.UUID, name, short string, position int) (*menu.Size, error) {
	row, err := models.New(r.pool).CreateMenuSize(ctx, models.CreateMenuSizeParams{TenantID: pgID(tenantID), LocationID: pgID(locationID),
		Name: name, Short: short, Position: int32(position)})
	if err != nil {
		return nil, err
	}
	s := sizeOf(row)
	return &s, nil
}

func (r *MenuBankRepository) ListSizes(ctx context.Context, locationID uuid.UUID) ([]menu.Size, error) {
	rows, err := models.New(r.pool).ListMenuSizes(ctx, pgID(locationID))
	if err != nil {
		return nil, err
	}
	out := make([]menu.Size, 0, len(rows))
	for _, s := range rows {
		out = append(out, sizeOf(s))
	}
	return out, nil
}

func (r *MenuBankRepository) GetSize(ctx context.Context, id, tenantID uuid.UUID) (*menu.Size, error) {
	row, err := models.New(r.pool).GetMenuSize(ctx, models.GetMenuSizeParams{ID: pgID(id), TenantID: pgID(tenantID)})
	if err != nil {
		return nil, err
	}
	s := sizeOf(row)
	return &s, nil
}

func (r *MenuBankRepository) UpdateSize(ctx context.Context, id, tenantID uuid.UUID, name, short string) (*menu.Size, error) {
	row, err := models.New(r.pool).UpdateMenuSize(ctx, models.UpdateMenuSizeParams{ID: pgID(id), TenantID: pgID(tenantID), Name: name, Short: short})
	if err != nil {
		return nil, err
	}
	s := sizeOf(row)
	return &s, nil
}

func (r *MenuBankRepository) ReorderSizes(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		q := models.New(tx)
		for i, id := range ids {
			if err := q.SetMenuSizePosition(ctx, models.SetMenuSizePositionParams{ID: pgID(id), TenantID: pgID(tenantID), Position: int32(i)}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *MenuBankRepository) DeleteSize(ctx context.Context, id, tenantID uuid.UUID) (bool, error) {
	n, err := models.New(r.pool).DeleteMenuSize(ctx, models.DeleteMenuSizeParams{ID: pgID(id), TenantID: pgID(tenantID)})
	return n > 0, err
}

// --- an item's / a group's bank data ---------------------------------------------

// SetItemBank replaces an item's kitchen name, SKU and sizes in one tx.
func (r *MenuBankRepository) SetItemBank(ctx context.Context, tenantID, locationID, itemID uuid.UUID, x menu.ItemExtra) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		q := models.New(tx)
		n, err := q.SetItemBank(ctx, models.SetItemBankParams{ID: pgID(itemID), TenantID: pgID(tenantID), KitchenName: x.KitchenName, Sku: x.SKU})
		if err != nil {
			return err
		}
		if n == 0 {
			return pgx.ErrNoRows
		}
		if err := q.DeleteItemSizes(ctx, models.DeleteItemSizesParams{ItemID: pgID(itemID), TenantID: pgID(tenantID)}); err != nil {
			return err
		}
		for i, s := range x.Sizes {
			if err := q.InsertItemSize(ctx, models.InsertItemSizeParams{TenantID: pgID(tenantID), LocationID: pgID(locationID), ItemID: pgID(itemID),
				SizeID: pgID(s.SizeID), PriceCents: s.PriceCents, KitchenName: s.KitchenName, Position: int32(i)}); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetGroupBank sets a group's portions flag and each listed option's default
// flag, kitchen name and per-size prices in one tx.
func (r *MenuBankRepository) SetGroupBank(ctx context.Context, tenantID, locationID, groupID uuid.UUID, x menu.GroupExtra) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		q := models.New(tx)
		n, err := q.SetGroupPortions(ctx, models.SetGroupPortionsParams{ID: pgID(groupID), TenantID: pgID(tenantID), Portions: x.Portions})
		if err != nil {
			return err
		}
		if n == 0 {
			return pgx.ErrNoRows
		}
		for _, o := range x.Options {
			n, err := q.SetOptionBank(ctx, models.SetOptionBankParams{ID: pgID(o.OptionID), TenantID: pgID(tenantID),
				IsDefault: o.IsDefault, KitchenName: o.KitchenName, GroupID: pgID(groupID)})
			if err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("option %s: %w", o.OptionID, pgx.ErrNoRows)
			}
			if err := q.DeleteOptionPrices(ctx, models.DeleteOptionPricesParams{OptionID: pgID(o.OptionID), TenantID: pgID(tenantID)}); err != nil {
				return err
			}
			for _, p := range o.Prices {
				if err := q.InsertOptionPrice(ctx, models.InsertOptionPriceParams{TenantID: pgID(tenantID), LocationID: pgID(locationID),
					OptionID: pgID(o.OptionID), SizeID: pgID(p.SizeID), PriceCents: p.PriceCents}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// --- views ---------------------------------------------------------------------

func viewOf(row models.MenuView) (*menuview.View, error) {
	v := &menuview.View{ID: idOf(row.ID), TenantID: idOf(row.TenantID), LocationID: idOf(row.LocationID),
		Kind: menuview.Kind(row.Kind), Name: row.Name, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}
	if err := json.Unmarshal(row.Rules, &v.Rules); err != nil {
		return nil, fmt.Errorf("view %s rules: %w", v.ID, err)
	}
	if v.Rules.HiddenCategories == nil {
		v.Rules.HiddenCategories = []uuid.UUID{}
	}
	if v.Rules.Round == "" {
		v.Rules.Round = menuview.RoundNone
	}
	return v, nil
}

func (r *MenuBankRepository) CreateView(ctx context.Context, tenantID, locationID uuid.UUID, kind menuview.Kind, name string, rules menuview.Rules) (*menuview.View, error) {
	raw, err := json.Marshal(rules)
	if err != nil {
		return nil, err
	}
	row, err := models.New(r.pool).CreateMenuView(ctx, models.CreateMenuViewParams{TenantID: pgID(tenantID), LocationID: pgID(locationID),
		Kind: string(kind), Name: name, Rules: raw})
	if err != nil {
		return nil, err
	}
	return viewOf(row)
}

func (r *MenuBankRepository) GetView(ctx context.Context, id uuid.UUID) (*menuview.View, error) {
	row, err := models.New(r.pool).GetMenuView(ctx, pgID(id))
	if err != nil {
		return nil, err
	}
	return viewOf(row)
}

func (r *MenuBankRepository) FirstViewOfKind(ctx context.Context, locationID uuid.UUID, kind menuview.Kind) (*menuview.View, error) {
	row, err := models.New(r.pool).FirstMenuViewOfKind(ctx, models.FirstMenuViewOfKindParams{LocationID: pgID(locationID), Kind: string(kind)})
	if err != nil {
		return nil, err
	}
	return viewOf(row)
}

func (r *MenuBankRepository) ListViews(ctx context.Context, locationID uuid.UUID) ([]menuview.View, error) {
	rows, err := models.New(r.pool).ListMenuViews(ctx, pgID(locationID))
	if err != nil {
		return nil, err
	}
	out := make([]menuview.View, 0, len(rows))
	for _, row := range rows {
		v, err := viewOf(row)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

func (r *MenuBankRepository) UpdateView(ctx context.Context, id uuid.UUID, name string, rules menuview.Rules) (*menuview.View, error) {
	raw, err := json.Marshal(rules)
	if err != nil {
		return nil, err
	}
	row, err := models.New(r.pool).UpdateMenuView(ctx, models.UpdateMenuViewParams{ID: pgID(id), Name: name, Rules: raw})
	if err != nil {
		return nil, err
	}
	return viewOf(row)
}

func (r *MenuBankRepository) DeleteView(ctx context.Context, id uuid.UUID) (bool, error) {
	n, err := models.New(r.pool).DeleteMenuView(ctx, pgID(id))
	return n > 0, err
}

// --- view entries ------------------------------------------------------------------

func entryOf(row models.MenuViewEntry) (*menuview.Entry, error) {
	e := &menuview.Entry{ID: idOf(row.ID), ViewID: idOf(row.ViewID), Kind: row.Kind, DisplayName: row.DisplayName,
		Description: row.Description, Photo: row.Photo, Category: row.Category, Position: int(row.Position), Hidden: row.Hidden,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}
	if row.PriceOverrideCents.Valid {
		v := row.PriceOverrideCents.Int64
		e.PriceOverrideCents = &v
	}
	if err := json.Unmarshal(row.Anchor, &e.Anchor); err != nil {
		return nil, fmt.Errorf("entry %s anchor: %w", e.ID, err)
	}
	if e.Anchor.ItemIDs == nil {
		e.Anchor.ItemIDs = []uuid.UUID{}
	}
	if err := json.Unmarshal(row.Extra, &e.Extra); err != nil {
		return nil, fmt.Errorf("entry %s extra: %w", e.ID, err)
	}
	return e, nil
}

func entryParams(e menuview.Entry) (anchor, extra []byte, price pgtype.Int8, err error) {
	if anchor, err = json.Marshal(e.Anchor); err != nil {
		return
	}
	if extra, err = json.Marshal(e.Extra); err != nil {
		return
	}
	if e.PriceOverrideCents != nil {
		price = pgtype.Int8{Int64: *e.PriceOverrideCents, Valid: true}
	}
	return
}

func (r *MenuBankRepository) CreateEntry(ctx context.Context, v *menuview.View, e menuview.Entry) (*menuview.Entry, error) {
	anchor, extra, price, err := entryParams(e)
	if err != nil {
		return nil, err
	}
	row, err := models.New(r.pool).CreateMenuViewEntry(ctx, models.CreateMenuViewEntryParams{TenantID: pgID(v.TenantID),
		LocationID: pgID(v.LocationID), ViewID: pgID(v.ID), Kind: e.Kind, Anchor: anchor, DisplayName: e.DisplayName,
		Description: e.Description, Photo: e.Photo, Category: e.Category, Position: int32(e.Position), Hidden: e.Hidden,
		PriceOverrideCents: price, Extra: extra})
	if err != nil {
		return nil, err
	}
	return entryOf(row)
}

func (r *MenuBankRepository) GetEntry(ctx context.Context, id uuid.UUID) (*menuview.Entry, error) {
	row, err := models.New(r.pool).GetMenuViewEntry(ctx, pgID(id))
	if err != nil {
		return nil, err
	}
	return entryOf(row)
}

func (r *MenuBankRepository) ListEntries(ctx context.Context, viewID uuid.UUID) ([]menuview.Entry, error) {
	rows, err := models.New(r.pool).ListMenuViewEntries(ctx, pgID(viewID))
	if err != nil {
		return nil, err
	}
	out := make([]menuview.Entry, 0, len(rows))
	for _, row := range rows {
		e, err := entryOf(row)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, nil
}

func (r *MenuBankRepository) UpdateEntry(ctx context.Context, e menuview.Entry) (*menuview.Entry, error) {
	anchor, extra, price, err := entryParams(e)
	if err != nil {
		return nil, err
	}
	row, err := models.New(r.pool).UpdateMenuViewEntry(ctx, models.UpdateMenuViewEntryParams{ID: pgID(e.ID), Anchor: anchor,
		DisplayName: e.DisplayName, Description: e.Description, Photo: e.Photo, Category: e.Category, Position: int32(e.Position),
		Hidden: e.Hidden, PriceOverrideCents: price, Extra: extra})
	if err != nil {
		return nil, err
	}
	return entryOf(row)
}

func (r *MenuBankRepository) DeleteEntry(ctx context.Context, id uuid.UUID) (bool, error) {
	n, err := models.New(r.pool).DeleteMenuViewEntry(ctx, pgID(id))
	return n > 0, err
}

// --- the build cache -----------------------------------------------------------------

// CachedBuild returns a view's cached build and the menu version it was
// compiled at (ok=false: none yet).
func (r *MenuBankRepository) CachedBuild(ctx context.Context, viewID uuid.UUID) (json.RawMessage, int64, bool, error) {
	row, err := models.New(r.pool).GetMenuViewBuild(ctx, pgID(viewID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	return row.Build, row.MenuVersion, true, nil
}

func (r *MenuBankRepository) SaveBuild(ctx context.Context, viewID uuid.UUID, version int64, build json.RawMessage) error {
	return models.New(r.pool).SaveMenuViewBuild(ctx, models.SaveMenuViewBuildParams{ViewID: pgID(viewID), MenuVersion: version, Build: build})
}
