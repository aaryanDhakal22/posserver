package repositories

import (
	"context"
	"time"

	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TrackingRepository persists channel tracking (main 3.0 step 6): each keyed
// channel's stream of order changes, the public tracking tokens and the
// not-accepted pages.
type TrackingRepository struct {
	pool *pgxpool.Pool
}

func NewTrackingRepository(pool *pgxpool.Pool) *TrackingRepository {
	return &TrackingRepository{pool: pool}
}

// TrackLink is an order's keyed-channel link.
type TrackLink struct {
	OrderID       uuid.UUID
	ChannelID     uuid.UUID
	ClientOrderID string
	TrackToken    string
}

// Link is the order's link to a channel that follows its orders: any but the
// built-in vendor feed (pgx.ErrNoRows = none).
func (r *TrackingRepository) Link(ctx context.Context, orderID uuid.UUID) (TrackLink, error) {
	row, err := models.New(r.pool).TrackingLink(ctx, pgID(orderID))
	if err != nil {
		return TrackLink{}, err
	}
	return TrackLink{OrderID: idOf(row.OrderID), ChannelID: idOf(row.ChannelID), ClientOrderID: row.ClientOrderID, TrackToken: row.TrackToken.String}, nil
}

// ByToken is the link holding a public tracking token (pgx.ErrNoRows = none).
func (r *TrackingRepository) ByToken(ctx context.Context, token string) (ChannelOrder, uuid.UUID, uuid.UUID, error) {
	row, err := models.New(r.pool).GetChannelOrderByToken(ctx, pgtype.Text{String: token, Valid: true})
	if err != nil {
		return ChannelOrder{}, uuid.Nil, uuid.Nil, err
	}
	return channelOrderOf(row), idOf(row.TenantID), idOf(row.LocationID), nil
}

func (r *TrackingRepository) InsertEvent(ctx context.Context, channelID, orderID uuid.UUID, typ string, payload []byte) (int64, error) {
	return models.New(r.pool).InsertChannelEvent(ctx, models.InsertChannelEventParams{ChannelID: pgID(channelID), OrderID: pgID(orderID), Type: typ, Payload: payload})
}

// StreamEvent is one stored stream event.
type StreamEvent struct {
	ID      int64
	Type    string
	Payload []byte
}

func (r *TrackingRepository) EventsAfter(ctx context.Context, channelID uuid.UUID, after int64, limit int) ([]StreamEvent, error) {
	rows, err := models.New(r.pool).ChannelEventsAfter(ctx, models.ChannelEventsAfterParams{ChannelID: pgID(channelID), ID: after, Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]StreamEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, StreamEvent{ID: row.ID, Type: row.Type, Payload: row.Payload})
	}
	return out, nil
}

// Latest is the channel's newest event id (0 = none).
func (r *TrackingRepository) Latest(ctx context.Context, channelID uuid.UUID) (int64, error) {
	return models.New(r.pool).LatestChannelEvent(ctx, pgID(channelID))
}

// Has reports whether the channel still keeps the event with that id.
func (r *TrackingRepository) Has(ctx context.Context, channelID uuid.UUID, id int64) (bool, error) {
	return models.New(r.pool).ChannelEventExists(ctx, models.ChannelEventExistsParams{ChannelID: pgID(channelID), ID: id})
}

func (r *TrackingRepository) Prune(ctx context.Context, before time.Time) (int64, error) {
	return models.New(r.pool).PruneChannelEvents(ctx, pgtype.Timestamptz{Time: before, Valid: true})
}

// AlertRow is a channel order to page about.
type AlertRow struct {
	OrderID, ChannelID, LocationID uuid.UUID
	ChannelName, Customer          string
}

func (r *TrackingRepository) ToAlert(ctx context.Context, placedBefore, placedAfter, dueBefore time.Time) ([]AlertRow, error) {
	rows, err := models.New(r.pool).ChannelOrdersToAlert(ctx, models.ChannelOrdersToAlertParams{
		PlacedBefore: pgtype.Timestamptz{Time: placedBefore, Valid: true}, PlacedAfter: pgtype.Timestamptz{Time: placedAfter, Valid: true},
		DueBefore: pgtype.Timestamptz{Time: dueBefore, Valid: true}})
	if err != nil {
		return nil, err
	}
	out := make([]AlertRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, AlertRow{OrderID: idOf(row.OrderID), ChannelID: idOf(row.ChannelID), LocationID: idOf(row.LocationID),
			ChannelName: row.ChannelName, Customer: row.CustomerDisplayName})
	}
	return out, nil
}

func (r *TrackingRepository) MarkAlerted(ctx context.Context, orderID uuid.UUID) error {
	return models.New(r.pool).MarkChannelOrderAlerted(ctx, pgID(orderID))
}
