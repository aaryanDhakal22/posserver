package repositories

import (
	"context"
	"encoding/json"
	"fmt"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DeliveryRunStore persists the agent's delivery.run floor events
// (floor.DeliveryRunStore).
type DeliveryRunStore struct{ pool *pgxpool.Pool }

func NewDeliveryRunStore(pool *pgxpool.Pool) *DeliveryRunStore { return &DeliveryRunStore{pool: pool} }

// SaveDeliveryRun marks the floor event seen and inserts the run (id = the
// event id) in one tx. A repeat is a duplicate.
func (s *DeliveryRunStore) SaveDeliveryRun(ctx context.Context, sc order.Scope, eventID uuid.UUID, r order.DeliveryRun) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := models.New(tx)
	tid, lid := toPgUUID(uuid.UUID(sc.TenantID)), toPgUUID(uuid.UUID(sc.LocationID))
	n, err := q.MarkFloorEventSeen(ctx, models.MarkFloorEventSeenParams{
		EventID: toPgUUID(eventID), TenantID: tid, LocationID: lid, Type: "delivery.run", Result: order.FloorApplied,
	})
	if err != nil {
		return "", err
	}
	if n == 0 {
		return order.FloorDuplicate, nil
	}
	ids := func(v []string) []byte {
		if v == nil {
			v = []string{}
		}
		b, _ := json.Marshal(v)
		return b
	}
	batch := pgtype.Int8{}
	if r.BatchID != nil {
		batch = pgtype.Int8{Int64: *r.BatchID, Valid: true}
	}
	if _, err := q.InsertDeliveryRun(ctx, models.InsertDeliveryRunParams{
		ID: toPgUUID(eventID), TenantID: tid, LocationID: lid, BatchID: batch,
		DriverID: r.DriverID, DriverName: r.DriverName, PlanID: r.PlanID,
		SuggestedIds: ids(r.SuggestedIDs), SentIds: ids(r.SentIDs), Stops: r.Stops,
		At: pgtype.Timestamptz{Time: r.At, Valid: true},
	}); err != nil {
		return "", fmt.Errorf("insert delivery run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return order.FloorApplied, nil
}

// ListDeliveryRuns returns a location's newest runs.
func (s *DeliveryRunStore) ListDeliveryRuns(ctx context.Context, sc order.Scope, limit int) ([]order.DeliveryRun, error) {
	rows, err := models.New(s.pool).ListDeliveryRuns(ctx, models.ListDeliveryRunsParams{
		TenantID: toPgUUID(uuid.UUID(sc.TenantID)), LocationID: toPgUUID(uuid.UUID(sc.LocationID)), Limit: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]order.DeliveryRun, 0, len(rows))
	for _, r := range rows {
		run := order.DeliveryRun{ID: uuid.UUID(r.ID.Bytes), DriverID: r.DriverID, DriverName: r.DriverName,
			PlanID: r.PlanID, Stops: r.Stops, At: r.At.Time}
		if r.BatchID.Valid {
			b := r.BatchID.Int64
			run.BatchID = &b
		}
		_ = json.Unmarshal(r.SuggestedIds, &run.SuggestedIDs)
		_ = json.Unmarshal(r.SentIds, &run.SentIDs)
		out = append(out, run)
	}
	return out, nil
}
