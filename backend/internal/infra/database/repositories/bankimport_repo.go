package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"quiccpos/main/internal/domain/bankdoc"
	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/menuview"
	"quiccpos/main/internal/domain/pricing"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BankImportRepository runs a bank import (bankdoc) in one transaction.
type BankImportRepository struct {
	pool *pgxpool.Pool
}

func NewBankImportRepository(pool *pgxpool.Pool) *BankImportRepository {
	return &BankImportRepository{pool: pool}
}

// ErrRollback, returned by fn, rolls the transaction back without an error
// (a dry run).
var ErrRollback = bankdoc.ErrRollback

// InTx runs fn in one transaction holding the location's import lock, so two
// imports of one store run one after the other. fn returning ErrRollback (or
// any error) rolls everything back; InTx then returns nil for ErrRollback.
func (r *BankImportRepository) InTx(ctx context.Context, tenantID, locationID uuid.UUID, fn func(bankdoc.Tx) error) error {
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "bank_import|"+locationID.String()); err != nil {
			return err
		}
		return fn(&bankTx{q: models.New(tx), tenant: tenantID, loc: locationID})
	})
	if errors.Is(err, ErrRollback) {
		return nil
	}
	return err
}

type bankTx struct {
	q           *models.Queries
	tenant, loc uuid.UUID
}

func (t *bankTx) T() pgtype.UUID { return pgID(t.tenant) }
func (t *bankTx) L() pgtype.UUID { return pgID(t.loc) }

// --- reads -------------------------------------------------------------------------

func (t *bankTx) Bank(ctx context.Context) (*menu.Bank, error) { return loadBank(ctx, t.q, t.loc) }

func (t *bankTx) Printers(ctx context.Context) ([]bankdoc.Printer, error) {
	rows, err := t.q.ListPrintersByLocation(ctx, t.L())
	if err != nil {
		return nil, err
	}
	out := make([]bankdoc.Printer, 0, len(rows))
	for _, p := range rows {
		out = append(out, bankdoc.Printer{ID: idOf(p.ID), Name: p.Name})
	}
	return out, nil
}

func (t *bankTx) Views(ctx context.Context) ([]menuview.View, error) {
	rows, err := t.q.ListMenuViews(ctx, t.L())
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

func (t *bankTx) Entries(ctx context.Context, viewID uuid.UUID) ([]menuview.Entry, error) {
	rows, err := t.q.ListMenuViewEntries(ctx, pgID(viewID))
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

func (t *bankTx) Deals(ctx context.Context) ([]pricing.StoreDeal, error) {
	rows, err := t.q.ListDeals(ctx, t.L())
	if err != nil {
		return nil, err
	}
	out := make([]pricing.StoreDeal, 0, len(rows))
	for _, row := range rows {
		d, err := dealOf(row)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// --- sizes -------------------------------------------------------------------------

func (t *bankTx) CreateSize(ctx context.Context, name, short string, position int) (uuid.UUID, error) {
	row, err := t.q.CreateMenuSize(ctx, models.CreateMenuSizeParams{TenantID: t.T(), LocationID: t.L(), Name: name, Short: short, Position: i32(position)})
	return idOf(row.ID), err
}

func (t *bankTx) UpdateSize(ctx context.Context, id uuid.UUID, name, short string, position int) error {
	if _, err := t.q.UpdateMenuSize(ctx, models.UpdateMenuSizeParams{ID: pgID(id), TenantID: t.T(), Name: name, Short: short}); err != nil {
		return err
	}
	return t.q.SetMenuSizePosition(ctx, models.SetMenuSizePositionParams{ID: pgID(id), TenantID: t.T(), Position: i32(position)})
}

func (t *bankTx) DeleteSize(ctx context.Context, id uuid.UUID) error {
	_, err := t.q.DeleteMenuSize(ctx, models.DeleteMenuSizeParams{ID: pgID(id), TenantID: t.T()})
	return err
}

// --- menus + categories ----------------------------------------------------------------

func (t *bankTx) CreateMenu(ctx context.Context, m menu.Menu) (uuid.UUID, error) {
	row, err := t.q.CreateMenu(ctx, models.CreateMenuParams{TenantID: t.T(), LocationID: t.L(), Name: m.Name, Description: m.Description,
		Available: m.Available, Position: i32(m.Position)})
	return idOf(row.ID), err
}

func (t *bankTx) UpdateMenu(ctx context.Context, m menu.Menu) error {
	if _, err := t.q.UpdateMenu(ctx, models.UpdateMenuParams{ID: pgID(m.ID), TenantID: t.T(), Name: m.Name, Description: m.Description,
		Available: m.Available}); err != nil {
		return err
	}
	return t.q.SetMenuPosition(ctx, models.SetMenuPositionParams{ID: pgID(m.ID), TenantID: t.T(), Position: i32(m.Position)})
}

func (t *bankTx) DeleteMenu(ctx context.Context, id uuid.UUID) error {
	return t.q.DeleteMenu(ctx, models.DeleteMenuParams{ID: pgID(id), TenantID: t.T()})
}

func (t *bankTx) CreateCategory(ctx context.Context, c menu.Category) (uuid.UUID, error) {
	row, err := t.q.CreateMenuCategory(ctx, models.CreateMenuCategoryParams{TenantID: t.T(), LocationID: t.L(), MenuID: pgID(c.MenuID),
		Name: c.Name, Description: c.Description, Available: c.Available, Position: i32(c.Position), DefaultPrinterID: pgIDPtr(c.DefaultPrinterID),
		NoKitchenTicket: c.NoKitchenTicket})
	return idOf(row.ID), err
}

func (t *bankTx) UpdateCategory(ctx context.Context, c menu.Category) error {
	if _, err := t.q.UpdateMenuCategory(ctx, models.UpdateMenuCategoryParams{ID: pgID(c.ID), TenantID: t.T(), Name: c.Name,
		Description: c.Description, Available: c.Available, DefaultPrinterID: pgIDPtr(c.DefaultPrinterID),
		NoKitchenTicket: c.NoKitchenTicket}); err != nil {
		return err
	}
	if err := t.q.MoveMenuCategory(ctx, models.MoveMenuCategoryParams{ID: pgID(c.ID), TenantID: t.T(), MenuID: pgID(c.MenuID)}); err != nil {
		return err
	}
	return t.q.SetCategoryPosition(ctx, models.SetCategoryPositionParams{ID: pgID(c.ID), TenantID: t.T(), Position: i32(c.Position)})
}

func (t *bankTx) DeleteCategory(ctx context.Context, id uuid.UUID) error {
	return t.q.DeleteMenuCategory(ctx, models.DeleteMenuCategoryParams{ID: pgID(id), TenantID: t.T()})
}

// --- items -------------------------------------------------------------------------

func (t *bankTx) CreateItem(ctx context.Context, it menu.Item) (uuid.UUID, error) {
	row, err := t.q.CreateMenuItem(ctx, models.CreateMenuItemParams{TenantID: t.T(), LocationID: t.L(), CategoryID: pgID(it.CategoryID),
		Name: it.Name, Description: it.Description, PriceCents: it.PriceCents, TaxRateBps: i32(it.TaxRateBps), InStock: it.InStock,
		OutUntil: tsOf(it.OutUntil), Tags: tagsToJSON(it.Tags), PrinterID: pgIDPtr(it.PrinterID), Position: i32(it.Position),
		Attributes: attrsToJSON(it.Attributes)})
	if err == nil && it.NoKitchenTicket {
		err = t.q.SetItemNoKitchen(ctx, models.SetItemNoKitchenParams{ID: row.ID, TenantID: t.T(), NoKitchenTicket: true})
	}
	return idOf(row.ID), err
}

func (t *bankTx) UpdateItem(ctx context.Context, it menu.Item) error {
	if _, err := t.q.UpdateMenuItem(ctx, models.UpdateMenuItemParams{ID: pgID(it.ID), TenantID: t.T(), Name: it.Name,
		Description: it.Description, PriceCents: it.PriceCents, TaxRateBps: i32(it.TaxRateBps), InStock: it.InStock,
		OutUntil: tsOf(it.OutUntil), Tags: tagsToJSON(it.Tags), Attributes: attrsToJSON(it.Attributes)}); err != nil {
		return err
	}
	if err := t.q.MoveMenuItem(ctx, models.MoveMenuItemParams{ID: pgID(it.ID), TenantID: t.T(), CategoryID: pgID(it.CategoryID)}); err != nil {
		return err
	}
	if err := t.q.SetItemPrinter(ctx, models.SetItemPrinterParams{ID: pgID(it.ID), TenantID: t.T(), PrinterID: pgIDPtr(it.PrinterID)}); err != nil {
		return err
	}
	if err := t.q.SetItemNoKitchen(ctx, models.SetItemNoKitchenParams{ID: pgID(it.ID), TenantID: t.T(), NoKitchenTicket: it.NoKitchenTicket}); err != nil {
		return err
	}
	return t.q.SetItemPosition(ctx, models.SetItemPositionParams{ID: pgID(it.ID), TenantID: t.T(), Position: i32(it.Position)})
}

func (t *bankTx) SetItemBank(ctx context.Context, id uuid.UUID, x menu.ItemExtra) error {
	n, err := t.q.SetItemBank(ctx, models.SetItemBankParams{ID: pgID(id), TenantID: t.T(), KitchenName: x.KitchenName, Sku: x.SKU})
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("item %s: %w", id, pgx.ErrNoRows)
	}
	if err := t.q.DeleteItemSizes(ctx, models.DeleteItemSizesParams{ItemID: pgID(id), TenantID: t.T()}); err != nil {
		return err
	}
	for i, s := range x.Sizes {
		if err := t.q.InsertItemSize(ctx, models.InsertItemSizeParams{TenantID: t.T(), LocationID: t.L(), ItemID: pgID(id),
			SizeID: pgID(s.SizeID), PriceCents: s.PriceCents, KitchenName: s.KitchenName, Position: i32(i)}); err != nil {
			return err
		}
	}
	return nil
}

// SetItemGroups makes the item's attached groups exactly these, in this order.
func (t *bankTx) SetItemGroups(ctx context.Context, id uuid.UUID, groups []uuid.UUID) error {
	cur, err := t.q.ListGroupsForItem(ctx, models.ListGroupsForItemParams{ItemID: pgID(id), TenantID: t.T()})
	if err != nil {
		return err
	}
	for _, g := range cur {
		if err := t.q.DetachModifierGroup(ctx, models.DetachModifierGroupParams{TenantID: t.T(), ItemID: pgID(id), GroupID: g.ID}); err != nil {
			return err
		}
	}
	for i, g := range groups {
		if err := t.q.AttachModifierGroup(ctx, models.AttachModifierGroupParams{TenantID: t.T(), ItemID: pgID(id), GroupID: pgID(g),
			Position: i32(i)}); err != nil {
			return err
		}
	}
	return nil
}

func (t *bankTx) DeleteItem(ctx context.Context, id uuid.UUID) error {
	return t.q.DeleteMenuItem(ctx, models.DeleteMenuItemParams{ID: pgID(id), TenantID: t.T()})
}

// --- groups + options --------------------------------------------------------------------

func (t *bankTx) CreateGroup(ctx context.Context, g bankdoc.GroupRow) (uuid.UUID, error) {
	row, err := t.q.CreateModifierGroup(ctx, models.CreateModifierGroupParams{TenantID: t.T(), LocationID: t.L(), Name: g.Name,
		Required: g.Required, MinSelect: i32(g.MinSelect), MaxSelect: i32(g.MaxSelect), AllowMultiple: g.AllowMultiple})
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := t.q.SetGroupPortions(ctx, models.SetGroupPortionsParams{ID: row.ID, TenantID: t.T(), Portions: g.Portions}); err != nil {
		return uuid.Nil, err
	}
	return idOf(row.ID), nil
}

func (t *bankTx) UpdateGroup(ctx context.Context, g bankdoc.GroupRow) error {
	if _, err := t.q.UpdateModifierGroup(ctx, models.UpdateModifierGroupParams{ID: pgID(g.ID), TenantID: t.T(), Name: g.Name,
		Required: g.Required, MinSelect: i32(g.MinSelect), MaxSelect: i32(g.MaxSelect), AllowMultiple: g.AllowMultiple}); err != nil {
		return err
	}
	_, err := t.q.SetGroupPortions(ctx, models.SetGroupPortionsParams{ID: pgID(g.ID), TenantID: t.T(), Portions: g.Portions})
	return err
}

func (t *bankTx) DeleteGroup(ctx context.Context, id uuid.UUID) error {
	return t.q.DeleteModifierGroup(ctx, models.DeleteModifierGroupParams{ID: pgID(id), TenantID: t.T()})
}

func (t *bankTx) CreateOption(ctx context.Context, o bankdoc.OptionRow) (uuid.UUID, error) {
	row, err := t.q.CreateModifierOption(ctx, models.CreateModifierOptionParams{TenantID: t.T(), LocationID: t.L(), GroupID: pgID(o.GroupID),
		Name: o.Name, PriceDeltaCents: o.PriceDeltaCents, InStock: o.InStock, Position: i32(o.Position)})
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := t.q.SetOptionBank(ctx, models.SetOptionBankParams{ID: row.ID, TenantID: t.T(), IsDefault: o.IsDefault,
		KitchenName: o.KitchenName, GroupID: pgID(o.GroupID)}); err != nil {
		return uuid.Nil, err
	}
	if o.ShowOnDriverTicket {
		if _, err := setDriverTicket(ctx, t.q.Raw(), t.loc, idOf(row.ID), true); err != nil {
			return uuid.Nil, err
		}
	}
	return idOf(row.ID), nil
}

func (t *bankTx) UpdateOption(ctx context.Context, o bankdoc.OptionRow) error {
	if _, err := t.q.UpdateModifierOption(ctx, models.UpdateModifierOptionParams{ID: pgID(o.ID), TenantID: t.T(), Name: o.Name,
		PriceDeltaCents: o.PriceDeltaCents, InStock: o.InStock}); err != nil {
		return err
	}
	if _, err := t.q.SetOptionBank(ctx, models.SetOptionBankParams{ID: pgID(o.ID), TenantID: t.T(), IsDefault: o.IsDefault,
		KitchenName: o.KitchenName, GroupID: pgID(o.GroupID)}); err != nil {
		return err
	}
	if _, err := setDriverTicket(ctx, t.q.Raw(), t.loc, o.ID, o.ShowOnDriverTicket); err != nil {
		return err
	}
	return t.q.SetOptionPosition(ctx, models.SetOptionPositionParams{ID: pgID(o.ID), TenantID: t.T(), Position: i32(o.Position)})
}

// SetOptionNeverIncluded sets an option's never_included flag (00009).
func (t *bankTx) SetOptionNeverIncluded(ctx context.Context, id uuid.UUID, on bool) error {
	_, err := setNeverIncluded(ctx, t.q.Raw(), t.loc, id, on)
	return err
}

func (t *bankTx) SetOptionPrices(ctx context.Context, id uuid.UUID, prices []menu.OptionPrice) error {
	if err := t.q.DeleteOptionPrices(ctx, models.DeleteOptionPricesParams{OptionID: pgID(id), TenantID: t.T()}); err != nil {
		return err
	}
	for _, p := range prices {
		if err := t.q.InsertOptionPrice(ctx, models.InsertOptionPriceParams{TenantID: t.T(), LocationID: t.L(), OptionID: pgID(id),
			SizeID: pgID(p.SizeID), PriceCents: p.PriceCents}); err != nil {
			return err
		}
	}
	return nil
}

func (t *bankTx) DeleteOption(ctx context.Context, id uuid.UUID) error {
	return t.q.DeleteModifierOption(ctx, models.DeleteModifierOptionParams{ID: pgID(id), TenantID: t.T()})
}

// --- views + entries --------------------------------------------------------------------

func (t *bankTx) CreateView(ctx context.Context, kind menuview.Kind, name string, rules menuview.Rules) (uuid.UUID, error) {
	raw, err := json.Marshal(rules)
	if err != nil {
		return uuid.Nil, err
	}
	row, err := t.q.CreateMenuView(ctx, models.CreateMenuViewParams{TenantID: t.T(), LocationID: t.L(), Kind: string(kind), Name: name, Rules: raw})
	return idOf(row.ID), err
}

func (t *bankTx) UpdateView(ctx context.Context, id uuid.UUID, name string, rules menuview.Rules) error {
	raw, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	_, err = t.q.UpdateMenuView(ctx, models.UpdateMenuViewParams{ID: pgID(id), Name: name, Rules: raw})
	return err
}

// ReplaceEntries deletes the view's entries and writes these.
func (t *bankTx) ReplaceEntries(ctx context.Context, v menuview.View, entries []menuview.Entry) error {
	cur, err := t.q.ListMenuViewEntries(ctx, pgID(v.ID))
	if err != nil {
		return err
	}
	for _, e := range cur {
		if _, err := t.q.DeleteMenuViewEntry(ctx, e.ID); err != nil {
			return err
		}
	}
	for _, e := range entries {
		anchor, extra, price, err := entryParams(e)
		if err != nil {
			return err
		}
		if _, err := t.q.CreateMenuViewEntry(ctx, models.CreateMenuViewEntryParams{TenantID: t.T(), LocationID: t.L(), ViewID: pgID(v.ID),
			Kind: e.Kind, Anchor: anchor, DisplayName: e.DisplayName, Description: e.Description, Photo: e.Photo, Category: e.Category,
			Position: i32(e.Position), Hidden: e.Hidden, PriceOverrideCents: price, Extra: extra}); err != nil {
			return err
		}
	}
	return nil
}

// --- deals -------------------------------------------------------------------------------

func (t *bankTx) CreateDeal(ctx context.Context, d pricing.StoreDeal) error {
	def, err := json.Marshal(d.Deal)
	if err != nil {
		return err
	}
	_, err = t.q.CreateDeal(ctx, models.CreateDealParams{TenantID: t.T(), LocationID: t.L(), Kind: d.Kind, Name: d.Name,
		Description: d.Description, Active: d.Active, ChannelKinds: d.ChannelKinds, Def: def, Code: d.Code})
	return codeTaken(err)
}

func (t *bankTx) UpdateDeal(ctx context.Context, id uuid.UUID, d pricing.StoreDeal) error {
	def, err := json.Marshal(d.Deal)
	if err != nil {
		return err
	}
	_, err = t.q.UpdateDeal(ctx, models.UpdateDealParams{ID: pgID(id), LocationID: t.L(), Kind: d.Kind, Name: d.Name,
		Description: d.Description, Active: d.Active, ChannelKinds: d.ChannelKinds, Def: def, Code: d.Code})
	return codeTaken(err)
}

func (t *bankTx) DeleteDeal(ctx context.Context, id uuid.UUID) error {
	_, err := t.q.DeleteDeal(ctx, models.DeleteDealParams{ID: pgID(id), LocationID: t.L()})
	return err
}

// --- families (00045) ---------------------------------------------------------------

func (t *bankTx) CreateFamily(ctx context.Context, f menu.Family) (uuid.UUID, error) {
	out, err := createFamily(ctx, t.q.Raw(), t.tenant, t.loc, f)
	return out.ID, err
}

func (t *bankTx) UpdateFamily(ctx context.Context, f menu.Family) error {
	_, err := updateFamily(ctx, t.q.Raw(), t.loc, f)
	return err
}

func (t *bankTx) DeleteFamily(ctx context.Context, id uuid.UUID) error {
	_, err := deleteFamily(ctx, t.q.Raw(), t.loc, id)
	return err
}

func (t *bankTx) SetFamily(ctx context.Context, option bool, ids []uuid.UUID, family *uuid.UUID) error {
	table := "menu_items"
	if option {
		table = "modifier_options"
	}
	_, err := setFamilies(ctx, t.q.Raw(), table, t.loc, ids, family)
	return err
}
