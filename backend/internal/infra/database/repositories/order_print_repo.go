package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OrderPrintStore keeps the Pi's order.printed jobs (floor.PrintStore;
// agent contract §2.10ag, main §2.5zn).
type OrderPrintStore struct{ pool *pgxpool.Pool }

func NewOrderPrintStore(pool *pgxpool.Pool) *OrderPrintStore { return &OrderPrintStore{pool: pool} }

// SaveOrderPrint marks the floor event seen and stores the job in one tx:
// applied; duplicate on a replay; not_found when the order isn't the
// store's (marked seen with that result, like any not_found).
func (s *OrderPrintStore) SaveOrderPrint(ctx context.Context, sc order.Scope, eventID, orderID uuid.UUID, p order.OrderPrint) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tid, lid := uuid.UUID(sc.TenantID), uuid.UUID(sc.LocationID)
	var one int
	err = tx.QueryRow(ctx, `SELECT 1 FROM orders WHERE id = $1 AND tenant_id = $2 AND location_id = $3`, orderID, tid, lid).Scan(&one)
	result := order.FloorApplied
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		result = order.FloorNotFound
	case err != nil:
		return "", fmt.Errorf("order: %w", err)
	}
	n, err := models.New(tx).MarkFloorEventSeen(ctx, models.MarkFloorEventSeenParams{
		EventID: toPgUUID(eventID), TenantID: toPgUUID(tid), LocationID: toPgUUID(lid), OrderID: optPgUUID(&orderID),
		Type: "order.printed", Result: result,
	})
	if err != nil {
		return "", err
	}
	if n == 0 {
		return order.FloorDuplicate, nil
	}
	if result == order.FloorApplied {
		raw, err := json.Marshal(p.Tickets)
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO order_prints (event_id, tenant_id, location_id, order_id, mode, tickets, at)
			VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (event_id) DO NOTHING`, eventID, tid, lid, orderID, p.Mode, raw, p.At); err != nil {
			return "", fmt.Errorf("insert order print: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return result, nil
}
