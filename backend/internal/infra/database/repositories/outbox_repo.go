package repositories

import (
	"context"
	"fmt"
	"time"

	"quiccpos/main/internal/dispatch"
	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// OutboxRepository serves the dispatcher: pulls pending events for a named
// handler, marks dispatches succeeded/failed, and exposes a depth gauge for
// observability. It does NOT insert events — that happens inside the order
// repo's tx so writes are atomic with the aggregate state.
type OutboxRepository struct {
	pool   *pgxpool.Pool
	logger zerolog.Logger
}

func NewOutboxRepository(pool *pgxpool.Pool, lg zerolog.Logger) *OutboxRepository {
	return &OutboxRepository{
		pool:   pool,
		logger: lg.With().Str("module", "outbox-repo").Logger(),
	}
}

// PendingEventsForHandler returns up to `limit` events the named handler
// has not yet successfully dispatched and isn't backed off from. Oldest
// occurred_at first, so dispatchers preserve causal order.
func (r *OutboxRepository) PendingEventsForHandler(ctx context.Context, handlerName string, limit int) ([]dispatch.PendingEvent, error) {
	q := models.New(r.pool)
	rows, err := q.PendingEventsForHandler(ctx, models.PendingEventsForHandlerParams{
		HandlerName: handlerName,
		Limit:       int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("pending events: %w", err)
	}
	out := make([]dispatch.PendingEvent, 0, len(rows))
	for _, row := range rows {
		// Pass the jsonb bytes through verbatim; handlers Unmarshal into
		// the right typed payload (Snapshot / StatusChangedPayload / …)
		// based on the event Type.
		out = append(out, dispatch.PendingEvent{
			Event: order.DomainEvent{
				ID:            uuid.UUID(row.ID.Bytes),
				AggregateID:   order.ID(row.AggregateID.Bytes),
				Type:          order.EventType(row.Type),
				Payload:       append([]byte(nil), row.Payload...),
				OccurredAt:    row.OccurredAt.Time,
				SchemaVersion: int(row.SchemaVersion),
			},
			TenantID: uuid.UUID(row.TenantID.Bytes),
			Attempts: int(row.Attempts),
		})
	}
	return out, nil
}

// ResetBackoffForScope makes every pending, backed-off dispatch row for one
// (tenant, location) due immediately — called when that scope's agent
// subscribes so its backlog isn't stuck behind exponential backoff.
func (r *OutboxRepository) ResetBackoffForScope(ctx context.Context, tenantID, locationID uuid.UUID) error {
	q := models.New(r.pool)
	if err := q.ResetDispatchBackoffForScope(ctx, models.ResetDispatchBackoffForScopeParams{
		TenantID:   pgtype.UUID{Bytes: tenantID, Valid: true},
		LocationID: pgtype.UUID{Bytes: locationID, Valid: true},
	}); err != nil {
		return fmt.Errorf("reset dispatch backoff: %w", err)
	}
	return nil
}

// MarkDispatched flips dispatched_at to NOW(); idempotent.
func (r *OutboxRepository) MarkDispatched(ctx context.Context, eventID uuid.UUID, handlerName string) error {
	q := models.New(r.pool)
	if err := q.MarkDispatchSucceeded(ctx, models.MarkDispatchSucceededParams{
		EventID:     pgtype.UUID{Bytes: eventID, Valid: true},
		HandlerName: handlerName,
	}); err != nil {
		return fmt.Errorf("mark dispatched: %w", err)
	}
	return nil
}

// MarkFailed records an error on the dispatch row and schedules a retry at
// nextAttempt. Pass zero time for "retry on next poll".
func (r *OutboxRepository) MarkFailed(ctx context.Context, eventID uuid.UUID, handlerName string, errMsg string, nextAttempt time.Time) error {
	var nextPg pgtype.Timestamptz
	if !nextAttempt.IsZero() {
		nextPg = pgtype.Timestamptz{Time: nextAttempt, Valid: true}
	}
	q := models.New(r.pool)
	if err := q.MarkDispatchFailed(ctx, models.MarkDispatchFailedParams{
		EventID:       pgtype.UUID{Bytes: eventID, Valid: true},
		HandlerName:   handlerName,
		LastError:     errMsg,
		NextAttemptAt: nextPg,
	}); err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	return nil
}

// CountPendingByTenant returns one handler's pending depth per tenant (every
// tenant, 0 when drained), used by the outbox.events.pending observable gauge.
func (r *OutboxRepository) CountPendingByTenant(ctx context.Context, handlerName string) (map[uuid.UUID]int, error) {
	rows, err := models.New(r.pool).CountPendingDispatchesByTenant(ctx, handlerName)
	if err != nil {
		return nil, fmt.Errorf("count pending: %w", err)
	}
	out := make(map[uuid.UUID]int, len(rows))
	for _, row := range rows {
		if row.TenantID.Valid {
			out[uuid.UUID(row.TenantID.Bytes)] = int(row.Pending)
		}
	}
	return out, nil
}
