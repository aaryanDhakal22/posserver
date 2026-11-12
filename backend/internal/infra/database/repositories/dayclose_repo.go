package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"quiccpos/main/internal/domain/dayclose"
	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DayCloseRepository keeps the Pi's cash drawer sessions, their pay-ins /
// pay-outs and the Z reports (terminal Stage 2, migration 00038). Plain SQL
// (not sqlc): the tables are its own. Each floor event is one transaction
// that writes its row(s) and marks the event seen with its result.
type DayCloseRepository struct{ pool *pgxpool.Pool }

func NewDayCloseRepository(pool *pgxpool.Pool) *DayCloseRepository {
	return &DayCloseRepository{pool: pool}
}

// NoRows reports a read that found no row (pgx.ErrNoRows).
func NoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// apply runs fn in a transaction and marks the floor event seen with its
// result; an event seen already answers duplicate (fn's writes rolled back).
func (r *DayCloseRepository) apply(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string,
	fn func(tx pgx.Tx) (string, error)) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	res, err := fn(tx)
	if err != nil {
		return "", err
	}
	fresh, err := markSeen(ctx, models.New(tx), sc, eventID, nil, typ, res)
	if err != nil {
		return "", err
	}
	if !fresh {
		return order.FloorDuplicate, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return res, nil
}

// drawerOwner is the store a drawer session belongs to (uuid.Nil = none).
func drawerOwner(ctx context.Context, tx pgx.Tx, id uuid.UUID) (uuid.UUID, bool, error) {
	var loc uuid.UUID
	var closed bool
	err := tx.QueryRow(ctx, `SELECT location_id, closed_at IS NOT NULL FROM drawer_sessions WHERE id = $1`, id).Scan(&loc, &closed)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	return loc, closed, err
}

// OpenDrawer stores drawer.opened. A session already closed stays as it is
// (stale); an id another store holds is invalid.
func (r *DayCloseRepository) OpenDrawer(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, d dayclose.Drawer) (string, error) {
	return r.apply(ctx, sc, eventID, typ, func(tx pgx.Tx) (string, error) {
		tag, err := tx.Exec(ctx, `INSERT INTO drawer_sessions (id, tenant_id, location_id, device_id, device_name, staff_id, staff_name,
				batch_id, opened_at, start_cents, handover_from)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (id) DO UPDATE SET device_id = EXCLUDED.device_id, device_name = EXCLUDED.device_name,
				staff_id = EXCLUDED.staff_id, staff_name = EXCLUDED.staff_name, batch_id = EXCLUDED.batch_id,
				opened_at = EXCLUDED.opened_at, start_cents = EXCLUDED.start_cents, handover_from = EXCLUDED.handover_from,
				updated_at = now()
			WHERE drawer_sessions.location_id = EXCLUDED.location_id AND drawer_sessions.closed_at IS NULL`,
			d.ID, uuid.UUID(sc.TenantID), uuid.UUID(sc.LocationID), d.DeviceID, d.DeviceName, d.StaffID, d.StaffName,
			d.BatchID, d.OpenedAt, d.StartCents, d.HandoverFrom)
		if err != nil {
			return "", fmt.Errorf("open drawer: %w", err)
		}
		if tag.RowsAffected() == 1 {
			return order.FloorApplied, nil
		}
		loc, _, err := drawerOwner(ctx, tx, d.ID)
		if err != nil {
			return "", err
		}
		if loc != uuid.UUID(sc.LocationID) {
			return order.FloorInvalid, nil
		}
		return order.FloorStale, nil // closed already: a late opened never reopens it
	})
}

// CloseDrawer stores drawer.closed: the whole session (it needs no
// drawer.opened before it).
func (r *DayCloseRepository) CloseDrawer(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, d dayclose.Drawer) (string, error) {
	return r.apply(ctx, sc, eventID, typ, func(tx pgx.Tx) (string, error) {
		var denoms []byte
		if d.Denominations != nil {
			denoms, _ = json.Marshal(d.Denominations)
		}
		tag, err := tx.Exec(ctx, `INSERT INTO drawer_sessions (id, tenant_id, location_id, device_id, device_name, staff_id, staff_name,
				batch_id, opened_at, start_cents, handover_from, closed_at, closed_by, close_reason, counted_cents, denominations,
				expected_cents, over_short_cents, flagged, cash_sales_cents, cash_refunds_cents, cash_tips_cents,
				pay_ins_cents, pay_outs_cents, no_sales, payments)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26)
			ON CONFLICT (id) DO UPDATE SET device_id = EXCLUDED.device_id, device_name = EXCLUDED.device_name,
				staff_id = EXCLUDED.staff_id, staff_name = EXCLUDED.staff_name, batch_id = EXCLUDED.batch_id,
				opened_at = EXCLUDED.opened_at, start_cents = EXCLUDED.start_cents, handover_from = EXCLUDED.handover_from,
				closed_at = EXCLUDED.closed_at, closed_by = EXCLUDED.closed_by, close_reason = EXCLUDED.close_reason,
				counted_cents = EXCLUDED.counted_cents, denominations = EXCLUDED.denominations,
				expected_cents = EXCLUDED.expected_cents, over_short_cents = EXCLUDED.over_short_cents, flagged = EXCLUDED.flagged,
				cash_sales_cents = EXCLUDED.cash_sales_cents, cash_refunds_cents = EXCLUDED.cash_refunds_cents,
				cash_tips_cents = EXCLUDED.cash_tips_cents, pay_ins_cents = EXCLUDED.pay_ins_cents,
				pay_outs_cents = EXCLUDED.pay_outs_cents, no_sales = EXCLUDED.no_sales, payments = EXCLUDED.payments,
				updated_at = now()
			WHERE drawer_sessions.location_id = EXCLUDED.location_id`,
			d.ID, uuid.UUID(sc.TenantID), uuid.UUID(sc.LocationID), d.DeviceID, d.DeviceName, d.StaffID, d.StaffName,
			d.BatchID, d.OpenedAt, d.StartCents, d.HandoverFrom, d.ClosedAt, d.ClosedBy, d.CloseReason, d.CountedCents, denoms,
			d.ExpectedCents, d.OverShortCents, d.Flagged, d.CashSalesCents, d.CashRefundsCents, d.CashTipsCents,
			d.PayInsCents, d.PayOutsCents, d.NoSales, d.Payments)
		if err != nil {
			return "", fmt.Errorf("close drawer: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return order.FloorInvalid, nil // another store's id
		}
		return order.FloorApplied, nil
	})
}

// AddMovement stores drawer.movement (no session row needed); a movement
// stored already is a duplicate.
func (r *DayCloseRepository) AddMovement(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, m dayclose.Movement) (string, error) {
	return r.apply(ctx, sc, eventID, typ, func(tx pgx.Tx) (string, error) {
		tag, err := tx.Exec(ctx, `INSERT INTO drawer_movements (id, tenant_id, location_id, drawer_id, kind, amount_cents, reason,
				staff_id, approved_by, at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT (id) DO NOTHING`,
			m.ID, uuid.UUID(sc.TenantID), uuid.UUID(sc.LocationID), m.DrawerID, m.Kind, m.AmountCents, m.Reason,
			m.StaffID, m.ApprovedBy, m.At)
		if err != nil {
			return "", fmt.Errorf("drawer movement: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return order.FloorDuplicate, nil
		}
		return order.FloorApplied, nil
	})
}

// SaveZ stores z.report: one per store per batch (a rebuilt Z replaces it).
// A report id another store or batch holds is invalid.
func (r *DayCloseRepository) SaveZ(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, z dayclose.Z) (string, error) {
	return r.apply(ctx, sc, eventID, typ, func(tx pgx.Tx) (string, error) {
		var loc uuid.UUID
		var batch int64
		err := tx.QueryRow(ctx, `SELECT location_id, batch_id FROM z_reports WHERE id = $1`, z.ID).Scan(&loc, &batch)
		switch {
		case err == nil && (loc != uuid.UUID(sc.LocationID) || batch != z.BatchID):
			return order.FloorInvalid, nil
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return "", err
		}
		_, err = tx.Exec(ctx, `INSERT INTO z_reports (id, tenant_id, location_id, batch_id, business_date, opened_at, closed_at, report)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (location_id, batch_id) DO UPDATE SET id = EXCLUDED.id, business_date = EXCLUDED.business_date,
				opened_at = EXCLUDED.opened_at, closed_at = EXCLUDED.closed_at, report = EXCLUDED.report, received_at = now()`,
			z.ID, uuid.UUID(sc.TenantID), uuid.UUID(sc.LocationID), z.BatchID, z.BusinessDate, z.OpenedAt, z.ClosedAt, []byte(z.Report))
		if err != nil {
			return "", fmt.Errorf("save z report: %w", err)
		}
		return order.FloorApplied, nil
	})
}

const zCols = `id, batch_id, business_date, opened_at, closed_at, report, received_at`

func scanZ(row pgx.Row) (dayclose.Z, error) {
	var z dayclose.Z
	var report []byte
	if err := row.Scan(&z.ID, &z.BatchID, &z.BusinessDate, &z.OpenedAt, &z.ClosedAt, &report, &z.ReceivedAt); err != nil {
		return z, err
	}
	z.Report = report
	return z, nil
}

// ListZ lists a store's Z reports, newest first (date nil = any date).
func (r *DayCloseRepository) ListZ(ctx context.Context, locationID uuid.UUID, date *time.Time, limit int) ([]dayclose.Z, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+zCols+` FROM z_reports
		WHERE location_id = $1 AND ($2::date IS NULL OR business_date = $2::date)
		ORDER BY closed_at DESC NULLS LAST, batch_id DESC LIMIT $3`, locationID, date, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []dayclose.Z{}
	for rows.Next() {
		z, err := scanZ(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, z)
	}
	return out, rows.Err()
}

// GetZ is one of the store's Z reports (pgx.ErrNoRows when it has none
// with that id).
func (r *DayCloseRepository) GetZ(ctx context.Context, locationID, id uuid.UUID) (dayclose.Z, error) {
	return scanZ(r.pool.QueryRow(ctx, `SELECT `+zCols+` FROM z_reports WHERE location_id = $1 AND id = $2`, locationID, id))
}

const drawerCols = `id, device_id, device_name, staff_id, staff_name, batch_id, opened_at, start_cents, handover_from, closed_at,
	closed_by, close_reason, counted_cents, denominations, expected_cents, over_short_cents, flagged, cash_sales_cents,
	cash_refunds_cents, cash_tips_cents, pay_ins_cents, pay_outs_cents, no_sales, payments, updated_at`

func scanDrawer(row pgx.Row) (dayclose.Drawer, error) {
	var d dayclose.Drawer
	var denoms []byte
	err := row.Scan(&d.ID, &d.DeviceID, &d.DeviceName, &d.StaffID, &d.StaffName, &d.BatchID, &d.OpenedAt, &d.StartCents,
		&d.HandoverFrom, &d.ClosedAt, &d.ClosedBy, &d.CloseReason, &d.CountedCents, &denoms, &d.ExpectedCents,
		&d.OverShortCents, &d.Flagged, &d.CashSalesCents, &d.CashRefundsCents, &d.CashTipsCents, &d.PayInsCents,
		&d.PayOutsCents, &d.NoSales, &d.Payments, &d.UpdatedAt)
	if err != nil {
		return d, err
	}
	if len(denoms) > 0 {
		if err := json.Unmarshal(denoms, &d.Denominations); err != nil {
			return d, fmt.Errorf("denominations: %w", err)
		}
	}
	return d, nil
}

// ListDrawers lists a store's drawer sessions, newest opened first (date
// nil = any; else sessions opened that store-local date).
func (r *DayCloseRepository) ListDrawers(ctx context.Context, locationID uuid.UUID, date *time.Time, limit int) ([]dayclose.Drawer, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+drawerCols+` FROM drawer_sessions d
		WHERE d.location_id = $1 AND ($2::date IS NULL OR
			(d.opened_at AT TIME ZONE (SELECT l.timezone FROM locations l WHERE l.id = d.location_id))::date = $2::date)
		ORDER BY d.opened_at DESC LIMIT $3`, locationID, date, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []dayclose.Drawer{}
	for rows.Next() {
		d, err := scanDrawer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDrawer is one of the store's drawer sessions with its movements
// (oldest first); pgx.ErrNoRows when the store has no such session.
func (r *DayCloseRepository) GetDrawer(ctx context.Context, locationID, id uuid.UUID) (dayclose.Drawer, []dayclose.Movement, error) {
	d, err := scanDrawer(r.pool.QueryRow(ctx, `SELECT `+drawerCols+` FROM drawer_sessions WHERE location_id = $1 AND id = $2`, locationID, id))
	if err != nil {
		return d, nil, err
	}
	rows, err := r.pool.Query(ctx, `SELECT id, drawer_id, kind, amount_cents, reason, staff_id, approved_by, at
		FROM drawer_movements WHERE location_id = $1 AND drawer_id = $2 ORDER BY at, id`, locationID, id)
	if err != nil {
		return d, nil, err
	}
	defer rows.Close()
	moves := []dayclose.Movement{}
	for rows.Next() {
		var m dayclose.Movement
		if err := rows.Scan(&m.ID, &m.DrawerID, &m.Kind, &m.AmountCents, &m.Reason, &m.StaffID, &m.ApprovedBy, &m.At); err != nil {
			return d, nil, err
		}
		moves = append(moves, m)
	}
	return d, moves, rows.Err()
}
