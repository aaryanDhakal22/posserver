package repositories

import (
	"context"
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
}

func holdOf(r models.CardHold) CardHold {
	return CardHold{OrderID: idOf(r.OrderID), TenantID: idOf(r.TenantID), LocationID: idOf(r.LocationID), Gateway: r.Gateway,
		PaymentRef: r.PaymentRef, AmountCents: r.AmountCents, TipCents: r.TipCents, AddedTipCents: r.AddedTipCents,
		Status: r.Status, CaptureReason: r.CaptureReason, DueAt: r.DueAt.Time, CaptureRequestedAt: tsPtr(r.CaptureRequestedAt),
		Attempts: int(r.Attempts), LastError: r.LastError, CapturedCents: r.CapturedCents, TipPaymentRef: r.TipPaymentRef,
		TipNote: r.TipNote, CapturedAt: tsPtr(r.CapturedAt), CancelledAt: tsPtr(r.CancelledAt), CreatedAt: r.CreatedAt.Time}
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

// Captured marks a hold captured and its order's card payment with it.
func (r *SquareRepository) Captured(ctx context.Context, orderID uuid.UUID, cents int64, tipRef, tipNote string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := models.New(tx)
	if err := q.CardHoldCaptured(ctx, models.CardHoldCapturedParams{OrderID: pgID(orderID), CapturedCents: cents,
		TipPaymentRef: tipRef, TipNote: tipNote}); err != nil {
		return err
	}
	if err := q.SetOrderCardPayment(ctx, models.SetOrderCardPaymentParams{OrderID: pgID(orderID), Status: "captured",
		AmountCents: cents, CapturedAt: ts(time.Now())}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Cancelled marks a hold voided (and its order's card payment refunded).
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
	if err := q.SetOrderCardPayment(ctx, models.SetOrderCardPaymentParams{OrderID: pgID(orderID), Status: "refunded",
		AmountCents: h.AmountCents + h.TipCents}); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
