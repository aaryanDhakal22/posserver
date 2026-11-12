package repositories

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// CallcenterLogin is a store's call-center login (its own username +
// password, valid only at /callcenter/v1/login).
type CallcenterLogin struct {
	TenantID     uuid.UUID
	LocationID   uuid.UUID
	Username     string
	PasswordHash string
	UpdatedAt    time.Time
}

// ErrCallcenterUsernameTaken: another store's call center has that username.
var ErrCallcenterUsernameTaken = errors.New("that call-center username is taken")

const ccLoginCols = `tenant_id, location_id, username, password_hash, updated_at`

func scanCCLogin(row pgx.Row) (CallcenterLogin, error) {
	var l CallcenterLogin
	err := row.Scan(&l.TenantID, &l.LocationID, &l.Username, &l.PasswordHash, &l.UpdatedAt)
	return l, err
}

// LoginByUsername is the call-center login with that username (any case);
// pgx.ErrNoRows when there is none.
func (r *CallcenterRepository) LoginByUsername(ctx context.Context, username string) (CallcenterLogin, error) {
	return scanCCLogin(r.pool.QueryRow(ctx, `SELECT `+ccLoginCols+` FROM callcenter_logins WHERE lower(username) = lower($1)`, username))
}

// LoginOf is the store's call-center login; pgx.ErrNoRows when none is set.
func (r *CallcenterRepository) LoginOf(ctx context.Context, locationID uuid.UUID) (CallcenterLogin, error) {
	return scanCCLogin(r.pool.QueryRow(ctx, `SELECT `+ccLoginCols+` FROM callcenter_logins WHERE location_id = $1`, locationID))
}

// revokeAll ends every live device of the store; the ids it ended.
func revokeAllCC(ctx context.Context, tx pgx.Tx, locationID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `UPDATE callcenter_devices SET revoked_at = now(), staff_id = NULL, session_started_at = NULL, session_seen_at = NULL
		WHERE location_id = $1 AND revoked_at IS NULL RETURNING id`, locationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetLogin saves the store's call-center login and, with revoke, ends every
// device signed in with the old one (one transaction). It returns the saved
// login and the devices it ended.
func (r *CallcenterRepository) SetLogin(ctx context.Context, tenantID, locationID uuid.UUID, username, passwordHash string, revoke bool) (CallcenterLogin, []uuid.UUID, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return CallcenterLogin{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	l, err := scanCCLogin(tx.QueryRow(ctx, `INSERT INTO callcenter_logins (location_id, tenant_id, username, password_hash)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (location_id) DO UPDATE SET username = EXCLUDED.username, password_hash = EXCLUDED.password_hash, updated_at = now()
		RETURNING `+ccLoginCols, locationID, tenantID, username, passwordHash))
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return CallcenterLogin{}, nil, ErrCallcenterUsernameTaken
		}
		return CallcenterLogin{}, nil, err
	}
	ids := []uuid.UUID{}
	if revoke {
		if ids, err = revokeAllCC(ctx, tx, locationID); err != nil {
			return CallcenterLogin{}, nil, err
		}
	}
	return l, ids, tx.Commit(ctx)
}

// DeleteLogin removes the store's call-center login and ends every device
// (nobody can sign the app in until a new one is set).
func (r *CallcenterRepository) DeleteLogin(ctx context.Context, locationID uuid.UUID) ([]uuid.UUID, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `DELETE FROM callcenter_logins WHERE location_id = $1`, locationID); err != nil {
		return nil, err
	}
	ids, err := revokeAllCC(ctx, tx, locationID)
	if err != nil {
		return nil, err
	}
	return ids, tx.Commit(ctx)
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

// PinAttempt counts one PIN attempt on the device BEFORE the PIN is checked,
// atomically (parallel guesses can't outrun it): the attempt is refused when
// the device is revoked or its pad is locked (ok = false; state says which);
// otherwise the count in the current window goes up by one (a window with no
// attempt for `window` starts again at 1) and, from the lockAfter-th on, the
// pad is locked at once for first·2^(n−lockAfter), at most maxWait. A right PIN
// takes its own attempt back (PinSucceeded); nothing else lowers the count.
func (r *CallcenterRepository) PinAttempt(ctx context.Context, id uuid.UUID, lockAfter int, first, maxWait, window time.Duration) (n int, ok bool, err error) {
	const counted = `(CASE WHEN pin_failed_at IS NULL OR pin_failed_at < now() - make_interval(secs => $5) THEN 0 ELSE pin_fails END) + 1`
	err = r.pool.QueryRow(ctx, `UPDATE callcenter_devices SET
			pin_fails = `+counted+`,
			pin_failed_at = now(),
			pin_locked_until = CASE WHEN `+counted+` >= $2
				THEN now() + make_interval(secs => LEAST($3 * power(2, LEAST(`+counted+` - $2, 20)), $4))
				ELSE NULL END
		WHERE id = $1 AND revoked_at IS NULL AND (pin_locked_until IS NULL OR pin_locked_until <= now())
		RETURNING pin_fails`, id, lockAfter, first.Seconds(), maxWait.Seconds(), window.Seconds()).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return n, err == nil, err
}

// PinState is the device's pad: live (not revoked) and locked until when.
func (r *CallcenterRepository) PinState(ctx context.Context, id uuid.UUID) (live bool, lockedUntil *time.Time, err error) {
	err = r.pool.QueryRow(ctx, `SELECT revoked_at IS NULL, pin_locked_until FROM callcenter_devices WHERE id = $1`, id).Scan(&live, &lockedUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, nil
	}
	return live, lockedUntil, err
}

// PinSucceeded signs the agent in after a right PIN: the attempt it counted is
// taken back (the wrong ones before it stay until the window passes) and, when
// that attempt had locked the pad (unlock), the lock is lifted. false = the
// device was revoked meanwhile: no session.
func (r *CallcenterRepository) PinSucceeded(ctx context.Context, id, staffID uuid.UUID, unlock bool) (bool, error) {
	tag, err := r.pool.Exec(ctx, `UPDATE callcenter_devices SET staff_id = $2, session_started_at = now(), session_seen_at = now(),
			last_seen_at = now(), pin_fails = GREATEST(pin_fails - 1, 0),
			pin_locked_until = CASE WHEN $3 THEN NULL ELSE pin_locked_until END
		WHERE id = $1 AND revoked_at IS NULL`, id, staffID, unlock)
	return tag.RowsAffected() > 0, err
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
