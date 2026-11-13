package repositories

import (
	"context"
	"fmt"

	menubulk "quiccpos/main/internal/app/menubulk"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MenuBulkRepository is Store › Menu's bulk edit (app/menubulk): plain SQL in
// one transaction, every id checked against the location first.
type MenuBulkRepository struct{ pool *pgxpool.Pool }

func NewMenuBulkRepository(pool *pgxpool.Pool) *MenuBulkRepository {
	return &MenuBulkRepository{pool: pool}
}

// idStrs is ids as text, each once (for $n::uuid[], as family_repo does).
func idStrs(ids []uuid.UUID) []string {
	seen := map[uuid.UUID]bool{}
	out := []string{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id.String())
		}
	}
	return out
}

// known returns which of ids are rows of table at the location.
func known(ctx context.Context, tx pgx.Tx, table string, locationID uuid.UUID, ids []string) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT id FROM `+table+` WHERE location_id = $1 AND id = ANY($2::uuid[])`, locationID, ids)
	if err != nil {
		return nil, fmt.Errorf("bulk %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ApplyItemChanges — see menubulk.Repo.
func (r *MenuBulkRepository) ApplyItemChanges(ctx context.Context, tenantID, locationID uuid.UUID, cs []menubulk.Resolved) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var items, cats, printers []uuid.UUID
	for _, c := range cs {
		items = append(items, c.ItemID)
		if c.CategoryID != nil {
			cats = append(cats, *c.CategoryID)
		}
		if c.Printer != nil {
			printers = append(printers, *c.Printer)
		}
	}
	// The items, with their Quick add flag and their sizes.
	type row struct {
		category uuid.UUID
		quickAdd bool
	}
	have := map[uuid.UUID]row{}
	rows, err := tx.Query(ctx, `SELECT id, category_id, quick_add FROM menu_items WHERE location_id = $1 AND tenant_id = $2 AND id = ANY($3::uuid[]) FOR UPDATE`,
		locationID, tenantID, idStrs(items))
	if err != nil {
		return fmt.Errorf("bulk items: %w", err)
	}
	for rows.Next() {
		var id uuid.UUID
		var x row
		if err := rows.Scan(&id, &x.category, &x.quickAdd); err != nil {
			rows.Close()
			return err
		}
		have[id] = x
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range cs {
		if _, ok := have[c.ItemID]; !ok {
			return fmt.Errorf("%w: item %s isn't this store's", menubulk.ErrNotFound, c.ItemID)
		}
	}
	okCats, err := known(ctx, tx, "menu_categories", locationID, idStrs(cats))
	if err != nil {
		return err
	}
	okPrinters, err := known(ctx, tx, "printers", locationID, idStrs(printers))
	if err != nil {
		return err
	}
	sizes := map[uuid.UUID]map[uuid.UUID]bool{}
	srows, err := tx.Query(ctx, `SELECT item_id, size_id FROM menu_item_sizes WHERE location_id = $1 AND item_id = ANY($2::uuid[])`, locationID, idStrs(items))
	if err != nil {
		return fmt.Errorf("bulk sizes: %w", err)
	}
	for srows.Next() {
		var it, sz uuid.UUID
		if err := srows.Scan(&it, &sz); err != nil {
			srows.Close()
			return err
		}
		if sizes[it] == nil {
			sizes[it] = map[uuid.UUID]bool{}
		}
		sizes[it][sz] = true
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return err
	}

	for _, c := range cs {
		cur := have[c.ItemID]
		moved := c.CategoryID != nil && *c.CategoryID != cur.category
		if c.CategoryID != nil && !okCats[*c.CategoryID] {
			return fmt.Errorf("%w: category %s isn't this store's", menubulk.ErrNotFound, *c.CategoryID)
		}
		if moved && cur.quickAdd {
			return fmt.Errorf("%w: the Quick add item stays in its own category", menubulk.ErrQuickAdd)
		}
		if c.Printer != nil && !okPrinters[*c.Printer] {
			return fmt.Errorf("%w: printer %s isn't this store's", menubulk.ErrNotFound, *c.Printer)
		}
		for sz := range c.Sizes {
			if !sizes[c.ItemID][sz] {
				return fmt.Errorf("%w: item %s doesn't come in size %s", menubulk.ErrUnknownSize, c.ItemID, sz)
			}
		}
		var newCat *uuid.UUID
		if moved {
			newCat = c.CategoryID
		}
		if _, err := tx.Exec(ctx, `UPDATE menu_items SET
				category_id = COALESCE($3, category_id),
				position = CASE WHEN $3::uuid IS NULL THEN position
					ELSE (SELECT COALESCE(MAX(position) + 1, 0) FROM menu_items WHERE category_id = $3) END,
				printer_id = CASE WHEN $4 THEN $5 ELSE printer_id END,
				no_kitchen_ticket = COALESCE($6, no_kitchen_ticket),
				in_stock = COALESCE($7, in_stock),
				out_until = CASE WHEN $7::boolean IS NULL THEN out_until ELSE NULL END,
				out_hold = CASE WHEN $7::boolean THEN false ELSE out_hold END,
				price_cents = COALESCE($8, price_cents),
				tax_rate_bps = COALESCE($9, tax_rate_bps),
				updated_at = now()
			WHERE id = $1 AND location_id = $2`,
			c.ItemID, locationID, newCat, c.SetPrinter, c.Printer, c.NoKitchenTicket, c.InStock, c.PriceCents, c.TaxRateBps); err != nil {
			return fmt.Errorf("bulk update item: %w", err)
		}
		for sz, cents := range c.Sizes {
			if _, err := tx.Exec(ctx, `UPDATE menu_item_sizes SET price_cents = $3 WHERE item_id = $1 AND size_id = $2`, c.ItemID, sz, cents); err != nil {
				return fmt.Errorf("bulk update size: %w", err)
			}
		}
	}
	return tx.Commit(ctx)
}
