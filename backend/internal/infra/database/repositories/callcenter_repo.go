package repositories

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CallcenterRepository keeps the call center's browsers (device tokens) and
// each one's agent session (CALLCENTER-PLAN.md, migration 00003, contract
// §2.5zh). Plain SQL: its table is its own.
type CallcenterRepository struct {
	pool *pgxpool.Pool
}

func NewCallcenterRepository(pool *pgxpool.Pool) *CallcenterRepository {
	return &CallcenterRepository{pool: pool}
}

// CallcenterDevice is one browser the store's login opened the app on.
type CallcenterDevice struct {
	ID               uuid.UUID
	TenantID         uuid.UUID
	LocationID       uuid.UUID
	Name             string
	CreatedAt        time.Time
	LastSeenAt       *time.Time
	RevokedAt        *time.Time
	StaffID          *uuid.UUID
	SessionStartedAt *time.Time
	SessionSeenAt    *time.Time
	// PinFails: wrong PINs in a row on this device; PinLockedUntil: no PIN
	// is tried before then.
	PinFails       int
	PinLockedUntil *time.Time
}

const ccCols = `id, tenant_id, location_id, name, created_at, last_seen_at, revoked_at, staff_id, session_started_at, session_seen_at, pin_fails, pin_locked_until`

func scanCC(row pgx.Row) (CallcenterDevice, error) {
	var d CallcenterDevice
	err := row.Scan(&d.ID, &d.TenantID, &d.LocationID, &d.Name, &d.CreatedAt, &d.LastSeenAt, &d.RevokedAt,
		&d.StaffID, &d.SessionStartedAt, &d.SessionSeenAt, &d.PinFails, &d.PinLockedUntil)
	return d, err
}

// ErrCallcenterTooMany: the store's live devices are at the cap.
var ErrCallcenterTooMany = errors.New("too many call-center devices")

// Create stores a new device (only its token's sha256), refusing past max
// live devices for the store.
func (r *CallcenterRepository) Create(ctx context.Context, tenantID, locationID uuid.UUID, name, tokenHash string, max int) (CallcenterDevice, error) {
	row := r.pool.QueryRow(ctx, `INSERT INTO callcenter_devices (tenant_id, location_id, name, token_hash, last_seen_at)
		SELECT $1, $2, $3, $4, now()
		WHERE (SELECT count(*) FROM callcenter_devices WHERE location_id = $2 AND revoked_at IS NULL) < $5
		RETURNING `+ccCols, tenantID, locationID, name, tokenHash, max)
	d, err := scanCC(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrCallcenterTooMany
	}
	return d, err
}

// ByTokenHash is the live device of a token (pgx.ErrNoRows: unknown or revoked).
func (r *CallcenterRepository) ByTokenHash(ctx context.Context, tokenHash string) (CallcenterDevice, error) {
	return scanCC(r.pool.QueryRow(ctx, `SELECT `+ccCols+` FROM callcenter_devices WHERE token_hash = $1 AND revoked_at IS NULL`, tokenHash))
}

// List is the store's devices, live first, newest first (revoked ones for 30 days).
func (r *CallcenterRepository) List(ctx context.Context, locationID uuid.UUID) ([]CallcenterDevice, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+ccCols+` FROM callcenter_devices
		WHERE location_id = $1 AND (revoked_at IS NULL OR revoked_at > now() - interval '30 days')
		ORDER BY (revoked_at IS NOT NULL), created_at DESC`, locationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CallcenterDevice{}
	for rows.Next() {
		d, err := scanCC(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Revoke ends a device (and its session); false = not this store's, or already revoked.
func (r *CallcenterRepository) Revoke(ctx context.Context, locationID, id uuid.UUID) (bool, error) {
	tag, err := r.pool.Exec(ctx, `UPDATE callcenter_devices SET revoked_at = now(), staff_id = NULL, session_started_at = NULL, session_seen_at = NULL
		WHERE id = $1 AND location_id = $2 AND revoked_at IS NULL`, id, locationID)
	return tag.RowsAffected() > 0, err
}

// Seen stamps the device (and, with session, its agent session) as active now.
func (r *CallcenterRepository) Seen(ctx context.Context, id uuid.UUID, session bool) error {
	q := `UPDATE callcenter_devices SET last_seen_at = now() WHERE id = $1`
	if session {
		q = `UPDATE callcenter_devices SET last_seen_at = now(), session_seen_at = now() WHERE id = $1 AND staff_id IS NOT NULL`
	}
	_, err := r.pool.Exec(ctx, q, id)
	return err
}

// StartSession signs an agent in on the device (replacing whoever was).
func (r *CallcenterRepository) StartSession(ctx context.Context, id, staffID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `UPDATE callcenter_devices SET staff_id = $2, session_started_at = now(), session_seen_at = now(), last_seen_at = now(),
		pin_fails = 0, pin_locked_until = NULL
		WHERE id = $1 AND revoked_at IS NULL`, id, staffID)
	return err
}

// PinFailed counts a wrong PIN on the device and, with a wait, locks the PIN
// pad until then. It returns the wrong PINs in a row so far.
func (r *CallcenterRepository) PinFailed(ctx context.Context, id uuid.UUID, lockUntil *time.Time) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `UPDATE callcenter_devices SET pin_fails = pin_fails + 1, pin_locked_until = COALESCE($2, pin_locked_until)
		WHERE id = $1 RETURNING pin_fails`, id, lockUntil).Scan(&n)
	return n, err
}

// EndSession locks the device; false = it had no session.
func (r *CallcenterRepository) EndSession(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := r.pool.Exec(ctx, `UPDATE callcenter_devices SET staff_id = NULL, session_started_at = NULL, session_seen_at = NULL
		WHERE id = $1 AND staff_id IS NOT NULL`, id)
	return tag.RowsAffected() > 0, err
}

// Sessions are the store's devices with an agent signed in.
func (r *CallcenterRepository) Sessions(ctx context.Context, locationID uuid.UUID) ([]CallcenterDevice, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+ccCols+` FROM callcenter_devices
		WHERE location_id = $1 AND revoked_at IS NULL AND staff_id IS NOT NULL`, locationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CallcenterDevice{}
	for rows.Next() {
		d, err := scanCC(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// CallcenterReportRow is one agent's day (contract §2.5zh "Report").
type CallcenterReportRow struct {
	Day        string `json:"day"`
	StaffID    string `json:"staff_id"`
	StaffName  string `json:"staff_name"`
	Orders     int    `json:"orders"`
	Voided     int    `json:"voided"`
	Pickup     int    `json:"pickup"`
	Delivery   int    `json:"delivery"`
	SalesCents int64  `json:"sales_cents"`
	CardCents  int64  `json:"card_cents"`
	TipsCents  int64  `json:"tips_cents"`
}

// Report: orders + sales per call-center agent per store-local day. An order
// counts for the agent who placed it (the snapshot's staff_id on an order
// placed by the Pi's call-center device), on the day it was placed.
func (r *CallcenterRepository) Report(ctx context.Context, locationID uuid.UUID, zone string, from, to string) ([]CallcenterReportRow, error) {
	rows, err := r.pool.Query(ctx, `
		WITH o AS (
			SELECT o.id, (o.placed_at AT TIME ZONE $2)::date AS day, o.source_metadata->>'staff_id' AS staff_id,
			       o.status IN ('cancelled', 'rejected') AS voided, o.fulfillment_type, o.total_cents
			FROM orders o
			WHERE o.location_id = $1 AND o.source = 'counter'
			  AND (o.source_metadata->>'device_kind' = 'callcenter' OR o.source_metadata->>'device_id' LIKE 'callcenter%')
			  AND (o.placed_at AT TIME ZONE $2)::date BETWEEN $3::date AND $4::date
		), p AS (
			SELECT p.order_id, sum(p.amount_cents) FILTER (WHERE p.processor IN ('manual_card', 'card', 'square_app')) AS card_cents,
			       sum(p.tip_cents) FILTER (WHERE p.amount_cents >= 0) AS tips_cents
			FROM payments p JOIN o ON o.id = p.order_id
			WHERE p.status IN ('captured', 'refunded')
			GROUP BY p.order_id
		)
		SELECT to_char(o.day, 'YYYY-MM-DD'), COALESCE(o.staff_id, ''),
		       COALESCE(trim(s.first_name || ' ' || CASE WHEN s.last_name <> '' THEN left(s.last_name, 1) || '.' ELSE '' END), ''),
		       count(*), count(*) FILTER (WHERE o.voided),
		       count(*) FILTER (WHERE NOT o.voided AND o.fulfillment_type <> 'delivery'),
		       count(*) FILTER (WHERE NOT o.voided AND o.fulfillment_type = 'delivery'),
		       COALESCE(sum(o.total_cents) FILTER (WHERE NOT o.voided), 0),
		       COALESCE(sum(p.card_cents), 0), COALESCE(sum(p.tips_cents), 0)
		FROM o LEFT JOIN p ON p.order_id = o.id
		LEFT JOIN staff s ON s.id::text = o.staff_id AND s.location_id = $1
		GROUP BY 1, 2, 3 ORDER BY 1 DESC, 3, 2`, locationID, zone, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CallcenterReportRow{}
	for rows.Next() {
		var x CallcenterReportRow
		if err := rows.Scan(&x.Day, &x.StaffID, &x.StaffName, &x.Orders, &x.Voided, &x.Pickup, &x.Delivery, &x.SalesCents, &x.CardCents, &x.TipsCents); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
