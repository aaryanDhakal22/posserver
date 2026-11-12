package repositories

import (
	"context"
	"time"

	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReceiptRepository is the durable queue of customer email receipts
// (weborder stage 2): one row per order and kind (placed / final).
type ReceiptRepository struct {
	pool *pgxpool.Pool
}

func NewReceiptRepository(pool *pgxpool.Pool) *ReceiptRepository {
	return &ReceiptRepository{pool: pool}
}

// Receipt is one queued email.
type Receipt struct {
	OrderID    uuid.UUID  `json:"order_id"`
	Kind       string     `json:"kind"`
	LocationID uuid.UUID  `json:"-"`
	Status     string     `json:"status"`
	Attempts   int        `json:"attempts"`
	LastError  string     `json:"last_error"`
	MessageID  string     `json:"message_id"`
	SentAt     *time.Time `json:"sent_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

func receiptOf(r models.EmailReceipt) Receipt {
	return Receipt{OrderID: idOf(r.OrderID), Kind: r.Kind, LocationID: idOf(r.LocationID), Status: r.Status, Attempts: int(r.Attempts),
		LastError: r.LastError, MessageID: r.MessageID, SentAt: tsPtr(r.SentAt), CreatedAt: r.CreatedAt.Time}
}

// QueueFinal queues the final receipt (only for an order that got one when placed).
func (r *ReceiptRepository) QueueFinal(ctx context.Context, orderID uuid.UUID) (bool, error) {
	n, err := models.New(r.pool).QueueFinalReceipt(ctx, pgID(orderID))
	return n > 0, err
}

// Claim leases up to n receipts due now (2 minutes).
func (r *ReceiptRepository) Claim(ctx context.Context, n int) ([]Receipt, error) {
	rows, err := models.New(r.pool).ClaimEmailReceipts(ctx, int32(n))
	if err != nil {
		return nil, err
	}
	out := make([]Receipt, 0, len(rows))
	for _, x := range rows {
		out = append(out, receiptOf(x))
	}
	return out, nil
}

// Done ends a receipt: sent / failed / skipped.
func (r *ReceiptRepository) Done(ctx context.Context, orderID uuid.UUID, kind, status, messageID, lastError string) error {
	return models.New(r.pool).EmailReceiptDone(ctx, models.EmailReceiptDoneParams{OrderID: pgID(orderID), Kind: kind, Status: status,
		MessageID: messageID, LastError: lastError})
}

func (r *ReceiptRepository) Retry(ctx context.Context, orderID uuid.UUID, kind string, next time.Time, lastError string) error {
	return models.New(r.pool).EmailReceiptRetry(ctx, models.EmailReceiptRetryParams{OrderID: pgID(orderID), Kind: kind,
		NextTryAt: ts(next), LastError: lastError})
}

// ForOrder lists an order's receipts.
func (r *ReceiptRepository) ForOrder(ctx context.Context, orderID uuid.UUID) ([]Receipt, error) {
	rows, err := models.New(r.pool).ListEmailReceipts(ctx, pgID(orderID))
	if err != nil {
		return nil, err
	}
	out := make([]Receipt, 0, len(rows))
	for _, x := range rows {
		out = append(out, receiptOf(x))
	}
	return out, nil
}
