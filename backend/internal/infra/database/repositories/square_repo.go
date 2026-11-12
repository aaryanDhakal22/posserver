package repositories

import (
	"context"
	"fmt"
	"strings"
	"time"

	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SquareRepository persists stores' Square connections and card holds
// (weborder stage 2). Tokens arrive and leave encrypted: this layer never
// sees them in the clear.
type SquareRepository struct {
	pool *pgxpool.Pool
}

func NewSquareRepository(pool *pgxpool.Pool) *SquareRepository { return &SquareRepository{pool: pool} }

// SquareConnection is a store's connected Square account.
type SquareConnection struct {
	LocationID       uuid.UUID
	TenantID         uuid.UUID
	MerchantID       string
	SquareLocationID string
	AccessEnc        []byte
	RefreshEnc       []byte
	ExpiresAt        time.Time
	RefreshedAt      time.Time
	Status           string
	LastError        string
	ConnectedAt      time.Time
}

func connOf(r models.SquareConnection) SquareConnection {
	return SquareConnection{LocationID: idOf(r.LocationID), TenantID: idOf(r.TenantID), MerchantID: r.MerchantID,
		SquareLocationID: r.SquareLocationID, AccessEnc: r.AccessTokenEnc, RefreshEnc: r.RefreshTokenEnc,
		ExpiresAt: r.ExpiresAt.Time, RefreshedAt: r.RefreshedAt.Time, Status: r.Status, LastError: r.LastError,
		ConnectedAt: r.ConnectedAt.Time}
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func (r *SquareRepository) Upsert(ctx context.Context, c SquareConnection) (SquareConnection, error) {
	row, err := models.New(r.pool).UpsertSquareConnection(ctx, models.UpsertSquareConnectionParams{
		LocationID: pgID(c.LocationID), TenantID: pgID(c.TenantID), MerchantID: c.MerchantID, SquareLocationID: c.SquareLocationID,
		AccessTokenEnc: c.AccessEnc, RefreshTokenEnc: c.RefreshEnc, ExpiresAt: ts(c.ExpiresAt)})
	if err != nil {
		return SquareConnection{}, err
	}
	return connOf(row), nil
}

// Get is the store's connection (pgx.ErrNoRows = none).
func (r *SquareRepository) Get(ctx context.Context, locationID uuid.UUID) (SquareConnection, error) {
	row, err := models.New(r.pool).GetSquareConnection(ctx, pgID(locationID))
	if err != nil {
		return SquareConnection{}, err
	}
	return connOf(row), nil
}

func (r *SquareRepository) ByMerchant(ctx context.Context, merchantID string) ([]SquareConnection, error) {
	rows, err := models.New(r.pool).SquareConnectionsByMerchant(ctx, merchantID)
	if err != nil {
		return nil, err
	}
	out := make([]SquareConnection, 0, len(rows))
	for _, x := range rows {
		out = append(out, connOf(x))
	}
	return out, nil
}

func (r *SquareRepository) SetLocation(ctx context.Context, locationID uuid.UUID, squareLocationID string) (bool, error) {
	n, err := models.New(r.pool).SetSquareLocation(ctx, models.SetSquareLocationParams{LocationID: pgID(locationID), SquareLocationID: squareLocationID})
	return n > 0, err
}

func (r *SquareRepository) UpdateTokens(ctx context.Context, locationID uuid.UUID, accessEnc, refreshEnc []byte, expires time.Time) error {
	return models.New(r.pool).UpdateSquareTokens(ctx, models.UpdateSquareTokensParams{LocationID: pgID(locationID),
		AccessTokenEnc: accessEnc, RefreshTokenEnc: refreshEnc, ExpiresAt: ts(expires)})
}

func (r *SquareRepository) SetStatus(ctx context.Context, locationID uuid.UUID, status, lastError string) error {
	return models.New(r.pool).SetSquareConnectionStatus(ctx, models.SetSquareConnectionStatusParams{LocationID: pgID(locationID),
		Status: status, LastError: lastError})
}

func (r *SquareRepository) Delete(ctx context.Context, locationID uuid.UUID) (bool, error) {
	n, err := models.New(r.pool).DeleteSquareConnection(ctx, pgID(locationID))
	return n > 0, err
}

// ToRefresh are connections whose token was last refreshed before `before`.
func (r *SquareRepository) ToRefresh(ctx context.Context, before time.Time) ([]SquareConnection, error) {
	rows, err := models.New(r.pool).SquareConnectionsToRefresh(ctx, ts(before))
	if err != nil {
		return nil, err
	}
	out := make([]SquareConnection, 0, len(rows))
	for _, x := range rows {
		out = append(out, connOf(x))
	}
	return out, nil
}

// --- card holds -----------------------------------------------------------------------

// CardHold is an online card order's hold.
type CardHold struct {
	OrderID            uuid.UUID  `json:"order_id"`
	TenantID           uuid.UUID  `json:"-"`
	LocationID         uuid.UUID  `json:"-"`
	Gateway            string     `json:"gateway"`
	PaymentRef         string     `json:"payment_ref"`
	AmountCents        int64      `json:"amount_cents"`
	TipCents           int64      `json:"tip_cents"`
	AddedTipCents      int64      `json:"added_tip_cents"`
	Status             string     `json:"status"`
	CaptureReason      string     `json:"capture_reason"`
	DueAt              time.Time  `json:"due_at"`
	CaptureRequestedAt *time.Time `json:"capture_requested_at"`
	Attempts           int        `json:"attempts"`
	LastError          string     `json:"last_error"`
	CapturedCents      int64      `json:"captured_cents"`
	TipPaymentRef      string     `json:"tip_payment_ref"`
	TipNote            string     `json:"tip_note"`
	CapturedAt         *time.Time `json:"captured_at"`
	CancelledAt        *time.Time `json:"cancelled_at"`
	CreatedAt          time.Time  `json:"created_at"`
	// 00043: the hold's payment row, the void / refund the worker owes (M-3).
	PaymentID         *uuid.UUID `json:"payment_id"`
	CancelRequestedAt *time.Time `json:"cancel_requested_at"`
	RefundRequestedAt *time.Time `json:"refund_requested_at"`
	RefundedCents     int64      `json:"refunded_cents"`
	RefundRef         string     `json:"refund_ref"`
	TipPaymentID      *uuid.UUID `json:"tip_payment_id"`
}

func uuidPtr(v pgtype.UUID) *uuid.UUID {
	if !v.Valid {
		return nil
	}
	id := uuid.UUID(v.Bytes)
	return &id
}

func holdOf(r models.CardHold) CardHold {
	return CardHold{OrderID: idOf(r.OrderID), TenantID: idOf(r.TenantID), LocationID: idOf(r.LocationID), Gateway: r.Gateway,
		PaymentRef: r.PaymentRef, AmountCents: r.AmountCents, TipCents: r.TipCents, AddedTipCents: r.AddedTipCents,
		Status: r.Status, CaptureReason: r.CaptureReason, DueAt: r.DueAt.Time, CaptureRequestedAt: tsPtr(r.CaptureRequestedAt),
		Attempts: int(r.Attempts), LastError: r.LastError, CapturedCents: r.CapturedCents, TipPaymentRef: r.TipPaymentRef,
		TipNote: r.TipNote, CapturedAt: tsPtr(r.CapturedAt), CancelledAt: tsPtr(r.CancelledAt), CreatedAt: r.CreatedAt.Time,
		PaymentID: uuidPtr(r.PaymentID), CancelRequestedAt: tsPtr(r.CancelRequestedAt), RefundRequestedAt: tsPtr(r.RefundRequestedAt),
		RefundedCents: r.RefundedCents, RefundRef: r.RefundRef, TipPaymentID: uuidPtr(r.TipPaymentID)}
}

// Hold is an order's hold (pgx.ErrNoRows = none).
func (r *SquareRepository) Hold(ctx context.Context, orderID uuid.UUID) (CardHold, error) {
	row, err := models.New(r.pool).GetCardHold(ctx, pgID(orderID))
	if err != nil {
		return CardHold{}, err
	}
	return holdOf(row), nil
}

func (r *SquareRepository) Holds(ctx context.Context, locationID uuid.UUID, limit int) ([]CardHold, error) {
	rows, err := models.New(r.pool).ListCardHolds(ctx, models.ListCardHoldsParams{LocationID: pgID(locationID), Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]CardHold, 0, len(rows))
	for _, x := range rows {
		out = append(out, holdOf(x))
	}
	return out, nil
}

// Claim leases up to n holds that are due for capture.
func (r *SquareRepository) Claim(ctx context.Context, n int) ([]CardHold, error) {
	rows, err := models.New(r.pool).ClaimCardHolds(ctx, int32(n))
	if err != nil {
		return nil, err
	}
	out := make([]CardHold, 0, len(rows))
	for _, x := range rows {
		out = append(out, holdOf(x))
	}
	return out, nil
}

func (r *SquareRepository) RequestCapture(ctx context.Context, locationID uuid.UUID, orderIDs []uuid.UUID, reason string) (int64, error) {
	ids := make([]pgtype.UUID, 0, len(orderIDs))
	for _, id := range orderIDs {
		ids = append(ids, pgID(id))
	}
	return models.New(r.pool).RequestCardHoldCapture(ctx, models.RequestCardHoldCaptureParams{LocationID: pgID(locationID),
		Column2: ids, CaptureReason: reason})
}

func (r *SquareRepository) RequestCaptureForBatch(ctx context.Context, locationID uuid.UUID, batchID int64) (int64, error) {
	return models.New(r.pool).RequestCardHoldCaptureForBatch(ctx, models.RequestCardHoldCaptureForBatchParams{
		LocationID: pgID(locationID), AgentBatchID: pgtype.Int8{Int64: batchID, Valid: true}})
}

func (r *SquareRepository) RequestCaptureForDriver(ctx context.Context, locationID uuid.UUID, driverID string, from, to time.Time) (int64, error) {
	return models.New(r.pool).RequestCardHoldCaptureForDriver(ctx, models.RequestCardHoldCaptureForDriverParams{
		LocationID: pgID(locationID), DriverID: driverID, At: ts(from), At_2: ts(to)})
}

// SetAddedTip sets the tip added after the order (false = no open hold).
func (r *SquareRepository) SetAddedTip(ctx context.Context, locationID, orderID uuid.UUID, cents int64) (bool, error) {
	n, err := models.New(r.pool).SetCardHoldAddedTip(ctx, models.SetCardHoldAddedTipParams{LocationID: pgID(locationID),
		OrderID: pgID(orderID), AddedTipCents: cents})
	return n > 0, err
}

// Captured marks a hold captured and its own payment row with it (M-4: by
// the hold's payment id, never every card row of the order). onHold is what
// was charged on the hold (its tip included: tip), separate the tip charged
// on its own payment (tipRef; 0 = none), which gets a payments row of its own
// (M-14). seenTip is the added tip the worker read: a tip set meanwhile makes
// this a no-op (false) and the capture is looked at again.
func (r *SquareRepository) Captured(ctx context.Context, h CardHold, onHold, tip, separate int64, tipRef, tipNote string, seenTip int64) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := models.New(tx)
	n, err := q.CardHoldCaptured(ctx, models.CardHoldCapturedParams{OrderID: pgID(h.OrderID), CapturedCents: onHold + separate,
		TipPaymentRef: tipRef, TipNote: tipNote, AddedTipCents: seenTip})
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	now := time.Now()
	if h.PaymentID != nil {
		if err := q.SetOrderCardPayment(ctx, models.SetOrderCardPaymentParams{ID: pgID(*h.PaymentID), Status: "captured",
			AmountCents: onHold, TipCents: tip, CapturedAt: ts(now)}); err != nil {
			return false, err
		}
	}
	if separate > 0 && tipRef != "" {
		id := uuid.Must(uuid.NewV7())
		if _, err := tx.Exec(ctx, `INSERT INTO payments (id, order_id, processor, status, amount_cents, token, captured_at, tip_cents, origin)
			VALUES ($1, $2, 'card', 'captured', $3, $4, $5, $3, 'card_hold_tip')`, id, h.OrderID, separate, tipRef, now); err != nil {
			return false, fmt.Errorf("tip payment: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE card_holds SET tip_payment_id = $2 WHERE order_id = $1`, h.OrderID, id); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}

// Cancelled marks a hold released (voided) and its own payment row voided.
func (r *SquareRepository) Cancelled(ctx context.Context, orderID uuid.UUID, note string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := models.New(tx)
	if err := q.CardHoldCancelled(ctx, models.CardHoldCancelledParams{OrderID: pgID(orderID), LastError: note}); err != nil {
		return err
	}
	h, err := q.GetCardHold(ctx, pgID(orderID))
	if err != nil {
		return err
	}
	if h.PaymentID.Valid {
		if err := q.SetOrderCardPayment(ctx, models.SetOrderCardPaymentParams{ID: h.PaymentID, Status: "voided",
			AmountCents: h.AmountCents + h.TipCents, TipCents: h.TipCents}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Refunded records the refund of a captured hold (a cancelled order): the
// hold is refunded and each refunded charge gets a negative payments row
// pointing at it (refund_of).
func (r *SquareRepository) Refunded(ctx context.Context, h CardHold, cents int64, ref string, tipCents int64, tipRef, note string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE card_holds SET status = 'refunded', refunded_cents = $2, refund_ref = $3, last_error = $4,
		cancelled_at = now(), updated_at = now() WHERE order_id = $1 AND status = 'captured'`, h.OrderID, cents+tipCents, ref, note); err != nil {
		return err
	}
	now := time.Now()
	add := func(of *uuid.UUID, amount int64, ref string) error {
		if amount <= 0 {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO payments (id, order_id, processor, status, amount_cents, token, captured_at, refund_of, origin)
			VALUES ($1, $2, 'card', 'refunded', $3, $4, $5, $6, 'card_hold_refund')`, uuid.Must(uuid.NewV7()), h.OrderID, -amount, ref, now, of)
		return err
	}
	if err := add(h.PaymentID, cents, ref); err != nil {
		return fmt.Errorf("refund payment: %w", err)
	}
	if err := add(h.TipPaymentID, tipCents, tipRef); err != nil {
		return fmt.Errorf("tip refund payment: %w", err)
	}
	return tx.Commit(ctx)
}

// ClaimUndo leases up to n holds whose void (held) or refund (captured) is
// owed: the order was cancelled (M-3).
func (r *SquareRepository) ClaimUndo(ctx context.Context, n int) ([]CardHold, error) {
	rows, err := r.pool.Query(ctx, `UPDATE card_holds SET next_try_at = now() + interval '2 minutes', updated_at = now()
		WHERE order_id IN (
			SELECT h.order_id FROM card_holds h
			WHERE h.next_try_at <= now() AND (
				(h.status = 'held' AND h.cancel_requested_at IS NOT NULL) OR
				(h.status = 'captured' AND (h.refund_requested_at IS NOT NULL OR h.cancel_requested_at IS NOT NULL)))
			ORDER BY h.next_try_at LIMIT $1 FOR UPDATE SKIP LOCKED)
		RETURNING *`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CardHold{}
	for rows.Next() {
		var x models.CardHold
		if err := rows.Scan(&x.OrderID, &x.TenantID, &x.LocationID, &x.Gateway, &x.PaymentRef, &x.AmountCents, &x.TipCents,
			&x.AddedTipCents, &x.Status, &x.CaptureReason, &x.DueAt, &x.CaptureRequestedAt, &x.NextTryAt, &x.Attempts,
			&x.LastError, &x.CapturedCents, &x.TipPaymentRef, &x.TipNote, &x.CapturedAt, &x.CancelledAt, &x.CreatedAt,
			&x.UpdatedAt, &x.PaymentID, &x.CancelRequestedAt, &x.RefundRequestedAt, &x.RefundedCents, &x.RefundRef,
			&x.TipPaymentID); err != nil {
			return nil, err
		}
		out = append(out, holdOf(x))
	}
	return out, rows.Err()
}

// UndoFailed stops retrying a void / refund (the owner settles it in Square).
func (r *SquareRepository) UndoFailed(ctx context.Context, orderID uuid.UUID, lastError string) error {
	_, err := r.pool.Exec(ctx, `UPDATE card_holds SET status = CASE WHEN status = 'held' THEN 'failed' ELSE status END,
		cancel_requested_at = NULL, refund_requested_at = NULL, attempts = attempts + 1, last_error = $2, updated_at = now()
		WHERE order_id = $1`, orderID, lastError)
	return err
}

// TipCap is the store's tip cap (the ordering setting tip_max_cents, default
// $50) for tips added after the order (M-20).
func (r *SquareRepository) TipCap(ctx context.Context, locationID uuid.UUID) (int64, error) {
	var c int64
	err := r.pool.QueryRow(ctx, `SELECT COALESCE((SELECT tip_max_cents FROM store_ordering WHERE location_id = $1), 5000)`, locationID).Scan(&c)
	return c, err
}

func (r *SquareRepository) Retry(ctx context.Context, orderID uuid.UUID, next time.Time, lastError string) error {
	return models.New(r.pool).CardHoldRetry(ctx, models.CardHoldRetryParams{OrderID: pgID(orderID), NextTryAt: ts(next), LastError: lastError})
}

func (r *SquareRepository) Failed(ctx context.Context, orderID uuid.UUID, lastError string) error {
	return models.New(r.pool).CardHoldFailed(ctx, models.CardHoldFailedParams{OrderID: pgID(orderID), LastError: lastError})
}

// SetDue moves a hold's safety capture time (dev tests).
func (r *SquareRepository) SetDue(ctx context.Context, orderID uuid.UUID, due time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE card_holds SET due_at = $2, next_try_at = LEAST(next_try_at, $2) WHERE order_id = $1`, pgID(orderID), ts(due))
	return err
}

// HoldByRef is a store's hold for a processor payment (pgx.ErrNoRows = none).
func (r *SquareRepository) HoldByRef(ctx context.Context, locationID uuid.UUID, ref string) (CardHold, error) {
	row, err := models.New(r.pool).GetCardHoldByRef(ctx, models.GetCardHoldByRefParams{LocationID: pgID(locationID), PaymentRef: ref})
	if err != nil {
		return CardHold{}, err
	}
	return holdOf(row), nil
}

// HoldRow is a hold as the Payments page lists it: with who ordered.
type HoldRow struct {
	CardHold
	OrderRef    string    `json:"order_ref"`
	Customer    string    `json:"customer"`
	Type        string    `json:"type"`
	PlacedAt    time.Time `json:"placed_at"`
	OrderStatus string    `json:"order_status"`
}

// HoldRows lists a store's holds with their orders, newest first ("" = any status).
func (r *SquareRepository) HoldRows(ctx context.Context, locationID uuid.UUID, status string, limit int) ([]HoldRow, error) {
	rows, err := models.New(r.pool).ListCardHoldsWithOrders(ctx, models.ListCardHoldsWithOrdersParams{LocationID: pgID(locationID),
		Limit: int32(limit), Status: pgtype.Text{String: status, Valid: status != ""}})
	if err != nil {
		return nil, err
	}
	out := make([]HoldRow, 0, len(rows))
	for _, x := range rows {
		h := holdOf(models.CardHold{OrderID: x.OrderID, TenantID: x.TenantID, LocationID: x.LocationID, Gateway: x.Gateway,
			PaymentRef: x.PaymentRef, AmountCents: x.AmountCents, TipCents: x.TipCents, AddedTipCents: x.AddedTipCents, Status: x.Status,
			CaptureReason: x.CaptureReason, DueAt: x.DueAt, CaptureRequestedAt: x.CaptureRequestedAt, NextTryAt: x.NextTryAt,
			Attempts: x.Attempts, LastError: x.LastError, CapturedCents: x.CapturedCents, TipPaymentRef: x.TipPaymentRef,
			TipNote: x.TipNote, CapturedAt: x.CapturedAt, CancelledAt: x.CancelledAt, CreatedAt: x.CreatedAt, UpdatedAt: x.UpdatedAt,
			PaymentID: x.PaymentID, CancelRequestedAt: x.CancelRequestedAt, RefundRequestedAt: x.RefundRequestedAt,
			RefundedCents: x.RefundedCents, RefundRef: x.RefundRef, TipPaymentID: x.TipPaymentID})
		id := h.OrderID.String()
		out = append(out, HoldRow{CardHold: h, OrderRef: "#" + strings.ToUpper(id[len(id)-6:]), Customer: x.CustomerDisplayName,
			Type: x.FulfillmentType, PlacedAt: x.PlacedAt.Time, OrderStatus: x.OrderStatus})
	}
	return out, nil
}
