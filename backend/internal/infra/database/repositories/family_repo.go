package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/pricing"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Families (migration 00045): plain SQL over models.Queries.Raw so the same
// statements run on the pool and inside the bank import's transaction.

// ErrFamilyNameTaken: another family of the store and kind has that name.
var ErrFamilyNameTaken = menu.ErrFamilyNameTaken

func familyErr(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return ErrFamilyNameTaken
	}
	return err
}

const familyCols = `id, location_id, kind, name, class, position`

func scanFamily(r pgx.Row) (menu.Family, error) {
	var f menu.Family
	err := r.Scan(&f.ID, &f.LocationID, &f.Kind, &f.Name, &f.Class, &f.Position)
	return f, err
}

// loadFamilies reads a location's families (by kind, position, name) and the
// family of every item and option that has one.
func loadFamilies(ctx context.Context, db models.DBTX, locationID uuid.UUID) ([]menu.Family, map[uuid.UUID]uuid.UUID, map[uuid.UUID]uuid.UUID, error) {
	rows, err := db.Query(ctx, `SELECT `+familyCols+` FROM menu_families WHERE location_id = $1 ORDER BY kind, position, lower(name), id`, locationID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("families: %w", err)
	}
	fams := []menu.Family{}
	for rows.Next() {
		f, err := scanFamily(rows)
		if err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		fams = append(fams, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	links := func(q string) (map[uuid.UUID]uuid.UUID, error) {
		rows, err := db.Query(ctx, q, locationID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[uuid.UUID]uuid.UUID{}
		for rows.Next() {
			var id, fam uuid.UUID
			if err := rows.Scan(&id, &fam); err != nil {
				return nil, err
			}
			out[id] = fam
		}
		return out, rows.Err()
	}
	items, err := links(`SELECT id, family_id FROM menu_items WHERE location_id = $1 AND family_id IS NOT NULL`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("item families: %w", err)
	}
	opts, err := links(`SELECT id, family_id FROM modifier_options WHERE location_id = $1 AND family_id IS NOT NULL`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("option families: %w", err)
	}
	return fams, items, opts, nil
}

func createFamily(ctx context.Context, db models.DBTX, tenantID, locationID uuid.UUID, f menu.Family) (menu.Family, error) {
	out, err := scanFamily(db.QueryRow(ctx, `INSERT INTO menu_families (tenant_id, location_id, kind, name, class, position)
		VALUES ($1, $2, $3, $4, $5, COALESCE((SELECT max(position) + 1 FROM menu_families WHERE location_id = $2 AND kind = $3), 0))
		RETURNING `+familyCols, tenantID, locationID, f.Kind, f.Name, f.Class))
	return out, familyErr(err)
}

func updateFamily(ctx context.Context, db models.DBTX, locationID uuid.UUID, f menu.Family) (menu.Family, error) {
	out, err := scanFamily(db.QueryRow(ctx, `UPDATE menu_families SET name = $3, class = $4, updated_at = now()
		WHERE location_id = $1 AND id = $2 RETURNING `+familyCols, locationID, f.ID, f.Name, f.Class))
	return out, familyErr(err)
}

func deleteFamily(ctx context.Context, db models.DBTX, locationID, id uuid.UUID) (bool, error) {
	tag, err := db.Exec(ctx, `DELETE FROM menu_families WHERE location_id = $1 AND id = $2`, locationID, id)
	return tag.RowsAffected() > 0, err
}

// setFamilies sets the family of the location's items (table menu_items) or
// options (modifier_options) among ids; only rows whose family differs are
// written. Returns how many changed.
func setFamilies(ctx context.Context, db models.DBTX, table string, locationID uuid.UUID, ids []uuid.UUID, family *uuid.UUID) (int, error) {
	if table != "menu_items" && table != "modifier_options" {
		return 0, fmt.Errorf("setFamilies: table %q", table)
	}
	strs := make([]string, len(ids))
	for i, id := range ids {
		strs[i] = id.String()
	}
	tag, err := db.Exec(ctx, `UPDATE `+table+` SET family_id = $3::uuid, updated_at = now()
		WHERE location_id = $1 AND id = ANY($2::uuid[]) AND family_id IS DISTINCT FROM $3::uuid`, locationID, strs, family)
	return int(tag.RowsAffected()), err
}

// FamilyRepository is the Families editor's store.
type FamilyRepository struct {
	pool *pgxpool.Pool
}

func NewFamilyRepository(pool *pgxpool.Pool) *FamilyRepository { return &FamilyRepository{pool: pool} }

// FamilyMember is menu.FamilyMember.
type FamilyMember = menu.FamilyMember

// List reads the location's families, items and options.
func (r *FamilyRepository) List(ctx context.Context, locationID uuid.UUID) ([]menu.Family, []FamilyMember, []FamilyMember, error) {
	fams, _, _, err := loadFamilies(ctx, r.pool, locationID)
	if err != nil {
		return nil, nil, nil, err
	}
	members := func(q string) ([]FamilyMember, error) {
		rows, err := r.pool.Query(ctx, q, locationID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []FamilyMember{}
		for rows.Next() {
			var m FamilyMember
			if err := rows.Scan(&m.ID, &m.Name, &m.Parent, &m.ParentID, &m.PriceCents, &m.FamilyID); err != nil {
				return nil, err
			}
			out = append(out, m)
		}
		return out, rows.Err()
	}
	items, err := members(`SELECT i.id, i.name, c.name, c.id, i.price_cents, i.family_id FROM menu_items i
		JOIN menu_categories c ON c.id = i.category_id WHERE i.location_id = $1 ORDER BY c.position, c.name, i.position, i.name, i.id`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("items: %w", err)
	}
	opts, err := members(`SELECT o.id, o.name, g.name, g.id, o.price_delta_cents, o.family_id FROM modifier_options o
		JOIN modifier_groups g ON g.id = o.group_id WHERE o.location_id = $1 ORDER BY g.name, g.id, o.position, o.name, o.id`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("options: %w", err)
	}
	return fams, items, opts, nil
}

func (r *FamilyRepository) Get(ctx context.Context, locationID, id uuid.UUID) (menu.Family, error) {
	return scanFamily(r.pool.QueryRow(ctx, `SELECT `+familyCols+` FROM menu_families WHERE location_id = $1 AND id = $2`, locationID, id))
}

func (r *FamilyRepository) Create(ctx context.Context, tenantID, locationID uuid.UUID, f menu.Family) (menu.Family, error) {
	return createFamily(ctx, r.pool, tenantID, locationID, f)
}

func (r *FamilyRepository) Update(ctx context.Context, locationID uuid.UUID, f menu.Family) (menu.Family, error) {
	return updateFamily(ctx, r.pool, locationID, f)
}

// Coupons lists the location's coupons (id, name, deal) for the family checks.
func couponDeals(ctx context.Context, db models.DBTX, locationID uuid.UUID, lock bool) ([]pricing.StoreDeal, error) {
	q := `SELECT id, name, def FROM deals WHERE location_id = $1 AND kind = 'coupon' ORDER BY position, created_at, id`
	if lock {
		q += ` FOR UPDATE`
	}
	rows, err := db.Query(ctx, q, locationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pricing.StoreDeal{}
	for rows.Next() {
		var id uuid.UUID
		var name string
		var def []byte
		if err := rows.Scan(&id, &name, &def); err != nil {
			return nil, err
		}
		var d pricing.StoreDeal
		if err := json.Unmarshal(def, &d.Deal); err != nil {
			return nil, fmt.Errorf("deal %s: %w", id, err)
		}
		d.ID, d.Name = id.String(), name
		out = append(out, d)
	}
	return out, rows.Err()
}

// CouponsNaming lists the names of the coupons whose parts name the family.
func (r *FamilyRepository) CouponsNaming(ctx context.Context, locationID, id uuid.UUID) ([]string, error) {
	ds, err := couponDeals(ctx, r.pool, locationID, false)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, d := range ds {
		if namesFamily(d.Deal, id.String()) {
			names = append(names, d.Name)
		}
	}
	return names, nil
}

func namesFamily(d pricing.Deal, id string) bool {
	for _, p := range d.Parts {
		for _, f := range p.Families {
			if f == id {
				return true
			}
		}
	}
	return false
}

func (r *FamilyRepository) Delete(ctx context.Context, locationID, id uuid.UUID) (bool, error) {
	return deleteFamily(ctx, r.pool, locationID, id)
}

// Merge moves from's items and options into into, makes every coupon part
// that names from name into instead, and deletes from — one transaction.
func (r *FamilyRepository) Merge(ctx context.Context, locationID, from, into uuid.UUID) (items, options, coupons int, err error) {
	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		t, err := tx.Exec(ctx, `UPDATE menu_items SET family_id = $3, updated_at = now() WHERE location_id = $1 AND family_id = $2`, locationID, from, into)
		if err != nil {
			return err
		}
		items = int(t.RowsAffected())
		t, err = tx.Exec(ctx, `UPDATE modifier_options SET family_id = $3, updated_at = now() WHERE location_id = $1 AND family_id = $2`, locationID, from, into)
		if err != nil {
			return err
		}
		options = int(t.RowsAffected())
		ds, err := couponDeals(ctx, tx, locationID, true)
		if err != nil {
			return err
		}
		for _, d := range ds {
			if !namesFamily(d.Deal, from.String()) {
				continue
			}
			for i := range d.Parts {
				fams := []string{}
				for _, f := range d.Parts[i].Families {
					if f == from.String() {
						f = into.String()
					}
					if !contains(fams, f) {
						fams = append(fams, f)
					}
				}
				d.Parts[i].Families = fams
			}
			d.Deal.ID = d.ID
			def, err := json.Marshal(d.Deal)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE deals SET def = $3, updated_at = now() WHERE location_id = $1 AND id = $2`, locationID, d.ID, def); err != nil {
				return err
			}
			coupons++
		}
		_, err = deleteFamily(ctx, tx, locationID, from)
		return err
	})
	return items, options, coupons, err
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Assign sets the family of items and options (nil = none) in one
// transaction; ids not the location's are left alone (the app checks them).
func (r *FamilyRepository) Assign(ctx context.Context, locationID uuid.UUID, family *uuid.UUID, items, options []uuid.UUID) (ni, no int, err error) {
	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		if len(items) > 0 {
			if ni, err = setFamilies(ctx, tx, "menu_items", locationID, items, family); err != nil {
				return err
			}
		}
		if len(options) > 0 {
			if no, err = setFamilies(ctx, tx, "modifier_options", locationID, options, family); err != nil {
				return err
			}
		}
		return nil
	})
	return ni, no, err
}

// ItemFamilies reads the location's families and each item's family (the menu CSV).
func (r *FamilyRepository) ItemFamilies(ctx context.Context, locationID uuid.UUID) ([]menu.Family, map[uuid.UUID]uuid.UUID, error) {
	fams, items, _, err := loadFamilies(ctx, r.pool, locationID)
	return fams, items, err
}
