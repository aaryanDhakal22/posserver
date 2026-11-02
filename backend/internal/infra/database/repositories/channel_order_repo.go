package repositories

import (
	"context"
	"time"

	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ChannelOrderRepository reads the channel ↔ order links (main 3.0 step 5;
// written by OrderRepository.Create in the order's transaction).
type ChannelOrderRepository struct {
	pool *pgxpool.Pool
}

func NewChannelOrderRepository(pool *pgxpool.Pool) *ChannelOrderRepository {
	return &ChannelOrderRepository{pool: pool}
}

// ChannelOrder is one link.
type ChannelOrder struct {
	OrderID       uuid.UUID
	ChannelID     *uuid.UUID
	ClientOrderID string
	Payment       []byte
	TrackToken    string
	CreatedAt     time.Time
}

func channelOrderOf(r models.ChannelOrder) ChannelOrder {
	return ChannelOrder{OrderID: idOf(r.OrderID), ChannelID: idPtrOf(r.ChannelID), ClientOrderID: r.ClientOrderID,
		Payment: r.Payment, TrackToken: r.TrackToken.String, CreatedAt: r.CreatedAt.Time}
}

// ByClient finds a channel's order by its idempotency key (pgx.ErrNoRows = none).
func (r *ChannelOrderRepository) ByClient(ctx context.Context, channelID uuid.UUID, clientOrderID string) (ChannelOrder, error) {
	row, err := models.New(r.pool).GetChannelOrderByClient(ctx, models.GetChannelOrderByClientParams{ChannelID: pgID(channelID), ClientOrderID: clientOrderID})
	if err != nil {
		return ChannelOrder{}, err
	}
	return channelOrderOf(row), nil
}

func (r *ChannelOrderRepository) Get(ctx context.Context, orderID uuid.UUID) (ChannelOrder, error) {
	row, err := models.New(r.pool).GetChannelOrder(ctx, pgID(orderID))
	if err != nil {
		return ChannelOrder{}, err
	}
	return channelOrderOf(row), nil
}

// List is a channel's orders, newest first, created before `before`.
func (r *ChannelOrderRepository) List(ctx context.Context, channelID uuid.UUID, before time.Time, limit int) ([]ChannelOrder, error) {
	rows, err := models.New(r.pool).ListChannelOrders(ctx, models.ListChannelOrdersParams{ChannelID: pgID(channelID),
		CreatedAt: pgtype.Timestamptz{Time: before, Valid: true}, Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]ChannelOrder, 0, len(rows))
	for _, row := range rows {
		out = append(out, channelOrderOf(row))
	}
	return out, nil
}
