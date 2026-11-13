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
// drawer.opened before it). A session closed already that closes again with
// other numbers keeps its old row in drawer_session_revisions and is flagged
// (M-9: never overwritten silently); revised reports that.
func (r *DayCloseRepository) CloseDrawer(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, d dayclose.Drawer) (res string, revised bool, err error) {
	res, err = r.apply(ctx, sc, eventID, typ, func(tx pgx.Tx) (string, error) {
		revised = false
		var old []byte
		var sameClose bool
		err := tx.QueryRow(ctx, `SELECT to_jsonb(d.*), (d.counted_cents IS NOT DISTINCT FROM $3 AND d.expected_cents IS NOT DISTINCT FROM $4
				AND d.over_short_cents IS NOT DISTINCT FROM $5 AND d.close_reason = $6 AND d.cash_sales_cents = $7 AND d.cash_refunds_cents = $8
				AND d.cash_tips_cents = $9 AND d.pay_ins_cents = $10 AND d.pay_outs_cents = $11)
			FROM drawer_sessions d WHERE d.id = $1 AND d.location_id = $2 AND d.closed_at IS NOT NULL FOR UPDATE`,
			d.ID, uuid.UUID(sc.LocationID), d.CountedCents, d.ExpectedCents, d.OverShortCents, d.CloseReason, d.CashSalesCents,
			d.CashRefundsCents, d.CashTipsCents, d.PayInsCents, d.PayOutsCents).Scan(&old, &sameClose)
		switch {
		case err == nil && !sameClose:
			if _, err := tx.Exec(ctx, `INSERT INTO drawer_session_revisions (drawer_id, tenant_id, location_id, event_id, old)
				VALUES ($1, $2, $3, $4, $5)`, d.ID, uuid.UUID(sc.TenantID), uuid.UUID(sc.LocationID), eventID, old); err != nil {
				return "", fmt.Errorf("drawer revision: %w", err)
			}
			revised = true
			if err := insertMoneyFlags(ctx, tx, sc, eventID, typ, "drawer:"+d.ID.String(), []order.MoneyFlag{{Code: order.FlagDrawerRecount,
				Detail: map[string]any{"previous": json.RawMessage(old)}}}); err != nil {
				return "", err
			}
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return "", fmt.Errorf("closed drawer: %w", err)
		}
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
			revised = false
			return order.FloorInvalid, nil // another store's id
		}
		return order.FloorApplied, nil
	})
	return res, revised && res == order.FloorApplied, err
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

// SaveZ stores z.report: one per store per batch. A rebuilt Z (another
// report for the batch) supersedes it, and the one it replaces is kept in
// z_report_versions (M-9). main checks the Z against the counter payments it
// has for the batch (M-12, main_check) and flags a Z that doesn't balance
// (the Pi's own reconcile) or doesn't match. A report id another store or
// batch holds is invalid.
func (r *DayCloseRepository) SaveZ(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, z dayclose.Z) (string, dayclose.Check, error) {
	var check dayclose.Check
	res, err := r.apply(ctx, sc, eventID, typ, func(tx pgx.Tx) (string, error) {
		var loc uuid.UUID
		var batch int64
		err := tx.QueryRow(ctx, `SELECT location_id, batch_id FROM z_reports WHERE id = $1`, z.ID).Scan(&loc, &batch)
		switch {
		case err == nil && (loc != uuid.UUID(sc.LocationID) || batch != z.BatchID):
			return order.FloorInvalid, nil
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return "", err
		}
		// The batch's Z so far (if any): kept as a version when this one differs.
		var curID uuid.UUID
		var curVersion int
		var same bool
		err = tx.QueryRow(ctx, `SELECT id, version, report = $3::jsonb FROM z_reports WHERE location_id = $1 AND batch_id = $2 FOR UPDATE`,
			uuid.UUID(sc.LocationID), z.BatchID, []byte(z.Report)).Scan(&curID, &curVersion, &same)
		version := 1
		switch {
		case err == nil && same && curID == z.ID:
			check, err = zCheck(ctx, tx, sc, z.BatchID)
			if err != nil {
				return "", err
			}
			check.Version = curVersion
			return order.FloorApplied, nil // the same report again under a new event: nothing changes
		case err == nil:
			if _, err := tx.Exec(ctx, `INSERT INTO z_report_versions (id, tenant_id, location_id, batch_id, version, business_date,
					opened_at, closed_at, report, received_at, superseded_by)
				SELECT id, tenant_id, location_id, batch_id, version, business_date, opened_at, closed_at, report, received_at, $3
				FROM z_reports WHERE location_id = $1 AND batch_id = $2`, uuid.UUID(sc.LocationID), z.BatchID, z.ID); err != nil {
				return "", fmt.Errorf("z version: %w", err)
			}
			version = curVersion + 1
		case !errors.Is(err, pgx.ErrNoRows):
			return "", fmt.Errorf("current z: %w", err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO z_reports (id, tenant_id, location_id, batch_id, business_date, opened_at, closed_at, report, version)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (location_id, batch_id) DO UPDATE SET id = EXCLUDED.id, business_date = EXCLUDED.business_date,
				opened_at = EXCLUDED.opened_at, closed_at = EXCLUDED.closed_at, report = EXCLUDED.report, version = EXCLUDED.version,
				received_at = now()`,
			z.ID, uuid.UUID(sc.TenantID), uuid.UUID(sc.LocationID), z.BatchID, z.BusinessDate, z.OpenedAt, z.ClosedAt, []byte(z.Report), version)
		if err != nil {
			return "", fmt.Errorf("save z report: %w", err)
		}
		check, err = zCheck(ctx, tx, sc, z.BatchID)
		if err != nil {
			return "", err
		}
		check.Version = version
		var flags []order.MoneyFlag
		if check.PiDifference != 0 {
			flags = append(flags, order.MoneyFlag{Code: order.FlagZUnbalanced, Detail: map[string]any{"difference_cents": check.PiDifference}})
		}
		if !check.Matches() {
			flags = append(flags, order.MoneyFlag{Code: order.FlagZMismatch, Detail: map[string]any{"tenders": check.Tenders,
				"difference_cents": check.MainDifference}})
		}
		for _, f := range flags {
			check.Flags = append(check.Flags, f.Code)
		}
		return order.FloorApplied, insertMoneyFlags(ctx, tx, sc, eventID, typ, fmt.Sprintf("z:%d", z.BatchID), flags)
	})
	if res != order.FloorApplied {
		check = dayclose.Check{}
	}
	return res, check, err
}

// zCheck compares the batch's Z (its tenders' net_cents) with the counter
// payments main has for the batch (Σ amount + tip per method) and stores
// the result on the Z (main_check, main_difference_cents, flagged).
func zCheck(ctx context.Context, tx pgx.Tx, sc order.Scope, batch int64) (dayclose.Check, error) {
	var report []byte
	if err := tx.QueryRow(ctx, `SELECT report FROM z_reports WHERE location_id = $1 AND batch_id = $2`, uuid.UUID(sc.LocationID), batch).
		Scan(&report); err != nil {
		return dayclose.Check{}, err
	}
	rows, err := tx.Query(ctx, `SELECT processor, COALESCE(sum(amount_cents + tip_cents), 0)::bigint FROM payments
		WHERE location_id = $1 AND origin = 'counter' AND batch_id = $2 GROUP BY processor`, uuid.UUID(sc.LocationID), batch)
	if err != nil {
		return dayclose.Check{}, err
	}
	mainNet := map[string]int64{}
	for rows.Next() {
		var m string
		var n int64
		if err := rows.Scan(&m, &n); err != nil {
			rows.Close()
			return dayclose.Check{}, err
		}
		mainNet[m] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return dayclose.Check{}, err
	}
	c := dayclose.CheckZ(report, mainNet)
	raw, _ := json.Marshal(c.Tenders)
	if _, err := tx.Exec(ctx, `UPDATE z_reports SET main_check = $3, main_difference_cents = $4, flagged = $5
		WHERE location_id = $1 AND batch_id = $2`, uuid.UUID(sc.LocationID), batch, raw, c.MainDifference, !c.Balanced()); err != nil {
		return dayclose.Check{}, fmt.Errorf("z check: %w", err)
	}
	return c, nil
}

// RecheckZ runs main's check again for a batch whose Z is stored (a counter
// payment of that batch arrived after it); a Z that matched and now doesn't
// is flagged. ok=false: the batch has no Z.
func RecheckZ(ctx context.Context, tx pgx.Tx, sc order.Scope, eventID uuid.UUID, typ string, batch int64) error {
	var had *int64
	err := tx.QueryRow(ctx, `SELECT main_difference_cents FROM z_reports WHERE location_id = $1 AND batch_id = $2 FOR UPDATE`,
		uuid.UUID(sc.LocationID), batch).Scan(&had)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	c, err := zCheck(ctx, tx, sc, batch)
	if err != nil {
		return err
	}
	if !c.Matches() && (had == nil || *had == 0) {
		return insertMoneyFlags(ctx, tx, sc, eventID, typ, fmt.Sprintf("z:%d", batch), []order.MoneyFlag{{Code: order.FlagZMismatch,
			Detail: map[string]any{"tenders": c.Tenders, "difference_cents": c.MainDifference, "late_payment": true}}})
	}
	return nil
}

const zCols = `id, batch_id, business_date, opened_at, closed_at, report, received_at, version, flagged, main_check, main_difference_cents`

func scanZ(row pgx.Row) (dayclose.Z, error) {
	var z dayclose.Z
	var report, check []byte
	if err := row.Scan(&z.ID, &z.BatchID, &z.BusinessDate, &z.OpenedAt, &z.ClosedAt, &report, &z.ReceivedAt, &z.Version, &z.Flagged,
		&check, &z.MainDifferenceCents); err != nil {
		return z, err
	}
	z.Report = report
	z.MainCheck = check
	return z, nil
}

// ZVersions lists the versions a batch's Z replaced, oldest first.
func (r *DayCloseRepository) ZVersions(ctx context.Context, locationID uuid.UUID, batch int64) ([]dayclose.ZVersion, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, version, received_at, superseded_at, superseded_by FROM z_report_versions
		WHERE location_id = $1 AND batch_id = $2 ORDER BY seq`, locationID, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []dayclose.ZVersion{}
	for rows.Next() {
		var v dayclose.ZVersion
		if err := rows.Scan(&v.ID, &v.Version, &v.ReceivedAt, &v.SupersededAt, &v.SupersededBy); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetZVersion is a superseded Z by its id (pgx.ErrNoRows = none).
func (r *DayCloseRepository) GetZVersion(ctx context.Context, locationID, id uuid.UUID) (dayclose.Z, dayclose.ZVersion, error) {
	var z dayclose.Z
	var v dayclose.ZVersion
	var report []byte
	err := r.pool.QueryRow(ctx, `SELECT id, batch_id, business_date, opened_at, closed_at, report, received_at, version,
			superseded_at, superseded_by
		FROM z_report_versions WHERE location_id = $1 AND id = $2 ORDER BY seq DESC LIMIT 1`, locationID, id).
		Scan(&z.ID, &z.BatchID, &z.BusinessDate, &z.OpenedAt, &z.ClosedAt, &report, &z.ReceivedAt, &z.Version, &v.SupersededAt, &v.SupersededBy)
	z.Report = report
	v.ID, v.Version, v.ReceivedAt = z.ID, z.Version, z.ReceivedAt
	return z, v, err
}

// DrawerRevisions lists what a closed drawer session was before each later
// drawer.closed changed it, oldest first.
func (r *DayCloseRepository) DrawerRevisions(ctx context.Context, locationID, drawerID uuid.UUID) ([]dayclose.Revision, error) {
	rows, err := r.pool.Query(ctx, `SELECT seq, old, revised_at FROM drawer_session_revisions
		WHERE location_id = $1 AND drawer_id = $2 ORDER BY seq`, locationID, drawerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []dayclose.Revision{}
	for rows.Next() {
		var v dayclose.Revision
		var old []byte
		if err := rows.Scan(&v.Seq, &old, &v.RevisedAt); err != nil {
			return nil, err
		}
		v.Before = old
		out = append(out, v)
	}
	return out, rows.Err()
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

// MoneyFlags lists a store's money flags, newest first (M-2 / M-7 / M-12 /
// M-13): what main stored but doesn't agree with.
func (r *DayCloseRepository) MoneyFlags(ctx context.Context, locationID uuid.UUID, limit int) ([]dayclose.Flag, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, at, event_id, event_type, subject, code, detail, resolved_at FROM money_flags
		WHERE location_id = $1 ORDER BY at DESC, id DESC LIMIT $2`, locationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []dayclose.Flag{}
	for rows.Next() {
		var f dayclose.Flag
		var detail []byte
		if err := rows.Scan(&f.ID, &f.At, &f.EventID, &f.EventType, &f.Subject, &f.Code, &detail, &f.ResolvedAt); err != nil {
			return nil, err
		}
		f.Detail = detail
		out = append(out, f)
	}
	return out, rows.Err()
}

// CardPayments lists the batch's card payments (counter origin: the Pi's
// terminal, keyed and Square app payments and their refunds), oldest first,
// with the brand and the order's number.
func (r *DayCloseRepository) CardPayments(ctx context.Context, locationID uuid.UUID, batch int64) ([]dayclose.CardPayment, error) {
	rows, err := r.pool.Query(ctx, `SELECT p.id::text, COALESCE(p.order_id::text, ''), p.processor, p.card_brand, p.last4,
		       p.amount_cents, p.tip_cents, COALESCE(p.captured_at, p.received_at), o.kitchen_order_number
		FROM payments p LEFT JOIN orders o ON o.id = p.order_id
		WHERE p.location_id = $1 AND p.batch_id = $2 AND p.origin = 'counter'
		  AND p.processor IN ('card', 'manual_card', 'square_app')
		ORDER BY COALESCE(p.captured_at, p.received_at), p.id LIMIT 1000`, locationID, batch)
	if err != nil {
		return nil, fmt.Errorf("card payments: %w", err)
	}
	defer rows.Close()
	out := []dayclose.CardPayment{}
	for rows.Next() {
		var c dayclose.CardPayment
		var num *int32
		if err := rows.Scan(&c.ID, &c.OrderID, &c.Method, &c.Brand, &c.Last4, &c.AmountCents, &c.TipCents, &c.At, &num); err != nil {
			return nil, err
		}
		if num != nil {
			n := int64(*num)
			c.OrderNumber = &n
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
