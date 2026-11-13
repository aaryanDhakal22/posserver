package repositories

import (
	"context"
	"errors"
	"fmt"

	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Quick add (00008): the location's one item with menu_items.quick_add, the
// route of the terminal's typed (Quicc Add) lines. Plain SQL, like the
// option flags (a full sqlc generate drifts the hand-kept models).

// quickAddOf reads the location's Quick add item (nil when there is none).
func quickAddOf(ctx context.Context, db models.DBTX, locationID uuid.UUID) (*menu.QuickAdd, error) {
	var qa menu.QuickAdd
	err := db.QueryRow(ctx, `SELECT i.id, i.category_id, c.menu_id
		FROM menu_items i JOIN menu_categories c ON c.id = i.category_id
		WHERE i.location_id = $1 AND i.quick_add`, locationID).Scan(&qa.ItemID, &qa.CategoryID, &qa.MenuID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("quick add: %w", err)
	}
	return &qa, nil
}

// QuickAdd — see menu.Repository.
func (r *MenuRepository) QuickAdd(ctx context.Context, locationID uuid.UUID) (*menu.QuickAdd, error) {
	return quickAddOf(ctx, r.pool, locationID)
}

// EnsureQuickAdd — see menu.Repository. Makes a "Quick add" menu (last:
// position 1000 or after the others) holding a "Quick add" category (no printer) with the $0 "Quick add"
// item, in one transaction under a per-location lock (two first pulls at
// once make one). The menu_version triggers bump the version: the Pi's next
// counter menu carries it.
func (r *MenuRepository) EnsureQuickAdd(ctx context.Context, tenantID, locationID uuid.UUID) (*menu.QuickAdd, error) {
	if qa, err := quickAddOf(ctx, r.pool, locationID); err != nil || qa != nil {
		return qa, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('menu_quick_add:' || $1::text, 0))`, locationID); err != nil {
		return nil, fmt.Errorf("quick add lock: %w", err)
	}
	if qa, err := quickAddOf(ctx, tx, locationID); err != nil || qa != nil {
		return qa, err
	}
	qa := menu.QuickAdd{}
	if err := tx.QueryRow(ctx, `INSERT INTO menus (tenant_id, location_id, name, description, available, position)
		VALUES ($1, $2, $3, 'The terminal''s Quick add: set where its typed items print.', true,
			(SELECT GREATEST(COALESCE(max(position), 0) + 1, 1000) FROM menus WHERE location_id = $2))
		RETURNING id`, tenantID, locationID, menu.QuickAddName).Scan(&qa.MenuID); err != nil {
		return nil, fmt.Errorf("quick add menu: %w", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO menu_categories (tenant_id, location_id, menu_id, name, available, position)
		VALUES ($1, $2, $3, $4, true, 0) RETURNING id`, tenantID, locationID, qa.MenuID, menu.QuickAddName).Scan(&qa.CategoryID); err != nil {
		return nil, fmt.Errorf("quick add category: %w", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO menu_items (tenant_id, location_id, category_id, name, description, price_cents, quick_add)
		VALUES ($1, $2, $3, $4, 'Typed at the terminal (name + price). Its printer decides where those lines print.', 0, true)
		RETURNING id`, tenantID, locationID, qa.CategoryID, menu.QuickAddName).Scan(&qa.ItemID); err != nil {
		return nil, fmt.Errorf("quick add item: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("quick add commit: %w", err)
	}
	return &qa, nil
}

// markQuickAdd sets Item.QuickAdd on the Quick add item among items.
func (r *MenuRepository) markQuickAdd(ctx context.Context, items []*menu.Item) error {
	if len(items) == 0 {
		return nil
	}
	qa, err := quickAddOf(ctx, r.pool, items[0].LocationID)
	if err != nil || qa == nil {
		return err
	}
	for _, it := range items {
		it.QuickAdd = it.ID == qa.ItemID
	}
	return nil
}
