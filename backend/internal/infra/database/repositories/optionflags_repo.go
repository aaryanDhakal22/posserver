package repositories

import (
	"context"
	"fmt"

	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Option flags (migration 00046): modifier_options.show_on_driver_ticket and
// "counts for coupons" (the option's mod family class is regular). Plain SQL
// over models.Queries.Raw like the families (00045), so the same statements
// run on the pool and inside the bank import's transaction.

// optionsOnDriverTicket is the set of the location's options flagged
// show_on_driver_ticket.
func optionsOnDriverTicket(ctx context.Context, db models.DBTX, locationID uuid.UUID) (map[uuid.UUID]bool, error) {
	rows, err := db.Query(ctx, `SELECT id FROM modifier_options WHERE location_id = $1 AND show_on_driver_ticket`, locationID)
	if err != nil {
		return nil, fmt.Errorf("driver ticket options: %w", err)
	}
	defer rows.Close()
	out := map[uuid.UUID]bool{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// setDriverTicket sets one option's flag (only written when it differs).
// Reports whether the location has that option.
func setDriverTicket(ctx context.Context, db models.DBTX, locationID, optionID uuid.UUID, on bool) (bool, error) {
	var found bool
	err := db.QueryRow(ctx, `WITH o AS (SELECT id FROM modifier_options WHERE location_id = $1 AND id = $2),
		u AS (UPDATE modifier_options SET show_on_driver_ticket = $3, updated_at = now()
			WHERE location_id = $1 AND id = $2 AND show_on_driver_ticket IS DISTINCT FROM $3)
		SELECT EXISTS (SELECT 1 FROM o)`, locationID, optionID, on).Scan(&found)
	return found, err
}

// OptionFlagsRepository backs app/optionflags.
type OptionFlagsRepository struct {
	pool *pgxpool.Pool
}

func NewOptionFlagsRepository(pool *pgxpool.Pool) *OptionFlagsRepository {
	return &OptionFlagsRepository{pool: pool}
}

// GroupOption is one option of a group with its family and flag.
type GroupOption = menu.GroupOption

// GroupOptions reads a group's options (in order) with their family and
// driver-ticket flag, and the location's families. ok is false when the
// location has no such group.
func groupOptions(ctx context.Context, db models.DBTX, locationID, groupID uuid.UUID, lock bool) ([]GroupOption, []menu.Family, bool, error) {
	var n int
	q := `SELECT count(*) FROM modifier_groups WHERE location_id = $1 AND id = $2`
	if err := db.QueryRow(ctx, q, locationID, groupID).Scan(&n); err != nil {
		return nil, nil, false, err
	}
	if n == 0 {
		return nil, nil, false, nil
	}
	sel := `SELECT id, name, family_id, show_on_driver_ticket FROM modifier_options
		WHERE location_id = $1 AND group_id = $2 ORDER BY position, created_at, id`
	if lock {
		sel += ` FOR UPDATE`
	}
	rows, err := db.Query(ctx, sel, locationID, groupID)
	if err != nil {
		return nil, nil, false, err
	}
	opts := []GroupOption{}
	for rows.Next() {
		var o GroupOption
		if err := rows.Scan(&o.ID, &o.Name, &o.FamilyID, &o.ShowOnDriverTicket); err != nil {
			rows.Close()
			return nil, nil, false, err
		}
		opts = append(opts, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, false, err
	}
	fams, _, _, err := loadFamilies(ctx, db, locationID)
	if err != nil {
		return nil, nil, false, err
	}
	return opts, fams, true, nil
}

// GroupOptions reads a group's options with their families (ok false: no
// such group in the location).
func (r *OptionFlagsRepository) GroupOptions(ctx context.Context, locationID, groupID uuid.UUID) ([]GroupOption, []menu.Family, bool, error) {
	return groupOptions(ctx, r.pool, locationID, groupID, false)
}

// SetDriverTicket sets one option's show_on_driver_ticket flag (ok false: no
// such option in the location).
func (r *OptionFlagsRepository) SetDriverTicket(ctx context.Context, locationID, optionID uuid.UUID, on bool) (bool, error) {
	return setDriverTicket(ctx, r.pool, locationID, optionID, on)
}

// SetOptionFamily runs decide over the group's options (locked) and the
// store's families in one transaction, then gives the option the family
// decide names (keep: nothing changes), creating it first when decide asks
// for a new one. It returns the option's family afterwards (nil = none) and
// ok false when the location has no such group.
func (r *OptionFlagsRepository) SetOptionFamily(ctx context.Context, tenantID, locationID, groupID, optionID uuid.UUID,
	decide func(opts []GroupOption, fams []menu.Family) (keep bool, family *uuid.UUID, create *menu.Family, err error)) (fam *menu.Family, ok bool, err error) {
	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		opts, fams, found, err := groupOptions(ctx, tx, locationID, groupID, true)
		if err != nil || !found {
			return err
		}
		ok = true
		keep, family, create, err := decide(opts, fams)
		if err != nil {
			return err
		}
		if keep {
			for _, o := range opts {
				if o.ID == optionID {
					family = o.FamilyID
				}
			}
		} else if create != nil {
			f, err := createFamily(ctx, tx, tenantID, locationID, *create)
			if err != nil {
				return err
			}
			family = &f.ID
			fams = append(fams, f)
		}
		if !keep {
			if _, err := setFamilies(ctx, tx, "modifier_options", locationID, []uuid.UUID{optionID}, family); err != nil {
				return err
			}
		}
		if family != nil {
			for i := range fams {
				if fams[i].ID == *family {
					f := fams[i]
					fam = &f
				}
			}
		}
		return nil
	})
	return fam, ok, err
}
