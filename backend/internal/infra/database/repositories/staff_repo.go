package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"quiccpos/main/internal/domain/staff"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StaffRepository keeps the counter terminal's staff, the store's terminal
// settings and the staff journal (TERMINAL-PLAN.md 0d, migration 00033).
// Plain SQL (not sqlc): its tables are its own.
type StaffRepository struct {
	pool *pgxpool.Pool
}

func NewStaffRepository(pool *pgxpool.Pool) *StaffRepository { return &StaffRepository{pool: pool} }

const staffCols = `id, tenant_id, location_id, first_name, last_name, pin_hash, role, limits, active, created_at, updated_at`

func scanPerson(row pgx.Row) (staff.Person, error) {
	var p staff.Person
	var limits []byte
	if err := row.Scan(&p.ID, &p.TenantID, &p.LocationID, &p.FirstName, &p.LastName, &p.PinHash, &p.Role, &limits,
		&p.Active, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return p, err
	}
	if len(limits) > 0 {
		if err := json.Unmarshal(limits, &p.Limits); err != nil {
			return p, fmt.Errorf("staff %s limits: %w", p.ID, err)
		}
	}
	return p, nil
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func listStaff(ctx context.Context, q querier, locationID uuid.UUID) ([]staff.Person, error) {
	rows, err := q.Query(ctx, `SELECT `+staffCols+` FROM staff WHERE location_id = $1
		ORDER BY active DESC, lower(first_name), lower(last_name), created_at`, locationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []staff.Person{}
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// List is the store's staff (with their PIN hashes), active first.
func (r *StaffRepository) List(ctx context.Context, locationID uuid.UUID) ([]staff.Person, error) {
	return listStaff(ctx, r.pool, locationID)
}

// Settings reads the store's terminal settings (nil = never saved) and its
// terminal version (0 = nothing written yet).
func (r *StaffRepository) Settings(ctx context.Context, locationID uuid.UUID) (*staff.Settings, int64, error) {
	var raw []byte
	var version int64
	err := r.pool.QueryRow(ctx, `SELECT settings, version FROM terminal_settings WHERE location_id = $1`, locationID).Scan(&raw, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if len(raw) == 0 || string(raw) == "{}" {
		return nil, version, nil
	}
	s := staff.DefaultSettings()
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, 0, fmt.Errorf("terminal settings: %w", err)
	}
	return &s, version, nil
}

// StaffTx is a staff write inside the store's lock (Write).
type StaffTx struct {
	tx         pgx.Tx
	tenantID   uuid.UUID
	locationID uuid.UUID
}

// Write runs fn in one transaction that first bumps the store's terminal
// version — the terminal_settings row lock also serializes the store's staff
// writes, so two PIN checks can't race. fn's error rolls everything back.
// Returns the new version.
func (r *StaffRepository) Write(ctx context.Context, tenantID, locationID uuid.UUID, fn func(ctx context.Context, tx staff.Tx) error) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var version int64
	if err := tx.QueryRow(ctx, `INSERT INTO terminal_settings (location_id, tenant_id, version) VALUES ($1, $2, 1)
		ON CONFLICT (location_id) DO UPDATE SET version = terminal_settings.version + 1, updated_at = now()
		RETURNING version`, locationID, tenantID).Scan(&version); err != nil {
		return 0, fmt.Errorf("terminal version: %w", err)
	}
	if err := fn(ctx, &StaffTx{tx: tx, tenantID: tenantID, locationID: locationID}); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return version, nil
}

// List is the store's staff, inside the write.
func (t *StaffTx) List(ctx context.Context) ([]staff.Person, error) {
	return listStaff(ctx, t.tx, t.locationID)
}

// Insert adds a person.
func (t *StaffTx) Insert(ctx context.Context, p staff.Person) (staff.Person, error) {
	limits, err := json.Marshal(p.Limits)
	if err != nil {
		return p, err
	}
	return scanPerson(t.tx.QueryRow(ctx, `INSERT INTO staff (tenant_id, location_id, first_name, last_name, pin_hash, role, limits, active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+staffCols,
		t.tenantID, t.locationID, p.FirstName, p.LastName, p.PinHash, p.Role, limits, p.Active))
}

// Update saves a person's name, role, limits, active flag and PIN hash.
func (t *StaffTx) Update(ctx context.Context, p staff.Person) (staff.Person, error) {
	limits, err := json.Marshal(p.Limits)
	if err != nil {
		return p, err
	}
	out, err := scanPerson(t.tx.QueryRow(ctx, `UPDATE staff SET first_name = $3, last_name = $4, pin_hash = $5, role = $6,
		limits = $7, active = $8, updated_at = now() WHERE id = $1 AND location_id = $2 RETURNING `+staffCols,
		p.ID, t.locationID, p.FirstName, p.LastName, p.PinHash, p.Role, limits, p.Active))
	if errors.Is(err, pgx.ErrNoRows) {
		return out, staff.ErrNotFound
	}
	return out, err
}

// Delete removes a person.
func (t *StaffTx) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := t.tx.Exec(ctx, `DELETE FROM staff WHERE id = $1 AND location_id = $2`, id, t.locationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return staff.ErrNotFound
	}
	return nil
}

// SaveSettings replaces the store's terminal settings.
func (t *StaffTx) SaveSettings(ctx context.Context, s staff.Settings) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(ctx, `UPDATE terminal_settings SET settings = $2, updated_at = now() WHERE location_id = $1`, t.locationID, raw)
	return err
}

// InsertJournal stores journal entries, skipping ids already there (a
// replayed batch). Returns how many were new.
func (r *StaffRepository) InsertJournal(ctx context.Context, tenantID, locationID uuid.UUID, entries []staff.JournalEntry) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	b := &pgx.Batch{}
	for _, e := range entries {
		detail := e.Detail
		if len(detail) == 0 {
			detail = []byte("{}")
		}
		b.Queue(`INSERT INTO staff_journal (id, tenant_id, location_id, at, staff_id, device_id, action, order_id, detail)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (id) DO NOTHING`,
			e.ID, tenantID, locationID, e.At, e.StaffID, e.DeviceID, e.Action, e.OrderID, detail)
	}
	res := r.pool.SendBatch(ctx, b)
	defer res.Close()
	n := 0
	for range entries {
		tag, err := res.Exec()
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// Journal lists the store's newest journal entries (before = older than,
// nil = the newest).
func (r *StaffRepository) Journal(ctx context.Context, locationID uuid.UUID, before *time.Time, limit int) ([]staff.JournalRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, at, staff_id, device_id, action, order_id, detail FROM staff_journal
		WHERE location_id = $1 AND ($2::timestamptz IS NULL OR at < $2) ORDER BY at DESC, id LIMIT $3`, locationID, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []staff.JournalRow{}
	for rows.Next() {
		var j staff.JournalRow
		var detail []byte
		if err := rows.Scan(&j.ID, &j.At, &j.StaffID, &j.DeviceID, &j.Action, &j.OrderID, &detail); err != nil {
			return nil, err
		}
		j.Detail = detail
		out = append(out, j)
	}
	return out, rows.Err()
}
