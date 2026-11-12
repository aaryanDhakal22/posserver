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
	"github.com/jackc/pgx/v5/pgtype"
)

// Counter orders from the Pi (TERMINAL-PLAN.md 0d, floor events
// order.placed / order.payment): each one is written in one transaction
// together with its floor_events_seen row.

func markSeen(ctx context.Context, q *models.Queries, sc order.Scope, eventID uuid.UUID, orderID *uuid.UUID, typ, result string) (bool, error) {
	n, err := q.MarkFloorEventSeen(ctx, models.MarkFloorEventSeenParams{
		EventID: toPgUUID(eventID), TenantID: toPgUUID(uuid.UUID(sc.TenantID)), LocationID: toPgUUID(uuid.UUID(sc.LocationID)),
		OrderID: optPgUUID(orderID), Type: typ, Result: result,
	})
	if err != nil {
		return false, fmt.Errorf("mark floor event: %w", err)
	}
	return n == 1, nil
}

func insertCounterPayment(ctx context.Context, tx pgx.Tx, orderID uuid.UUID, p order.CheckedPayment) (bool, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO payments (id, order_id, processor, status, amount_cents, token, last4, captured_at, auth_code, tip_cents, staff_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT (id) DO NOTHING`,
		p.ID, orderID, p.Method, string(p.Status), int64(p.Amount), p.Ref, p.Last4, p.At, p.AuthCode, int64(p.Tip), p.StaffID)
	if err != nil {
		return false, fmt.Errorf("insert payment: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// CreateCounterOrder stores the order the Pi placed: applied, or duplicate
// (the event was seen, or the order — by id or by its source_event_id — is
// there already). The order is saved acked (never re-sent to the Pi) with
// the Pi's floor state; agentStatus / batchID are the Pi's restore data.
func (r *OrderRepository) CreateCounterOrder(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, co *order.CounterOrder,
	agentStatus []byte, batchID *int64) (string, error) {
	o := co.Order
	oid := uuid.UUID(o.ID())
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := models.New(tx)

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM orders WHERE id = $1
		OR (tenant_id = $2 AND source = $3 AND source_event_id = $4))`,
		oid, uuid.UUID(sc.TenantID), string(order.SourceCounter), o.SourceEventID()).Scan(&exists); err != nil {
		return "", fmt.Errorf("order exists: %w", err)
	}
	result := order.FloorApplied
	if exists {
		result = order.FloorDuplicate
	}
	fresh, err := markSeen(ctx, q, sc, eventID, &oid, typ, result)
	if err != nil {
		return "", err
	}
	if !fresh {
		return order.FloorDuplicate, nil
	}
	if exists {
		return order.FloorDuplicate, tx.Commit(ctx)
	}

	// The Pi's floor state (accepted + the counter number by default).
	o.ApplyFloorUpdate(co.Floor)
	if err := r.createInTx(ctx, q, o); err != nil {
		return "", err
	}
	for _, p := range co.Payments {
		if _, err := insertCounterPayment(ctx, tx, oid, p); err != nil {
			return "", err
		}
	}
	var kitchen pgtype.Int4
	if k := o.KitchenNumber(); k != nil {
		kitchen = pgtype.Int4{Int32: int32(*k), Valid: true}
	}
	var eta, floorAt pgtype.Timestamptz
	if t := o.EstimatedReadyAt(); t != nil {
		eta = pgtype.Timestamptz{Time: *t, Valid: true}
	}
	if t := o.FloorAt(); t != nil {
		floorAt = pgtype.Timestamptz{Time: *t, Valid: true}
	}
	if err := q.UpdateOrderFloor(ctx, models.UpdateOrderFloorParams{
		ID: toPgUUID(oid), Status: string(o.Status()), KitchenOrderNumber: kitchen, EstimatedReadyAt: eta,
		DriverName: o.DriverName(), UpdatedAt: pgtype.Timestamptz{Time: o.UpdatedAt(), Valid: true}, FloorAt: floorAt,
	}); err != nil {
		return "", fmt.Errorf("update order floor: %w", err)
	}
	// The Pi made it: never re-sent (RunRedeliver only looks at unacked rows).
	if _, err := tx.Exec(ctx, `UPDATE orders SET agent_ack_at = now() WHERE id = $1`, oid); err != nil {
		return "", fmt.Errorf("ack: %w", err)
	}
	if len(agentStatus) > 0 && string(agentStatus) != "null" {
		batch := pgtype.Int8{}
		if batchID != nil {
			batch = pgtype.Int8{Int64: *batchID, Valid: true}
		}
		if _, err := q.SetOrderAgentStatus(ctx, models.SetOrderAgentStatusParams{
			AgentStatus: agentStatus, At: pgtype.Timestamptz{Time: o.PlacedAt(), Valid: true}, BatchID: batch,
			ID: toPgUUID(oid), TenantID: toPgUUID(uuid.UUID(sc.TenantID)), LocationID: toPgUUID(uuid.UUID(sc.LocationID)),
		}); err != nil {
			return "", fmt.Errorf("agent status: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return order.FloorApplied, nil
}

// AddCounterPayment adds a payment (or refund) to a counter order: applied,
// duplicate (the event was seen or the payment id is there), not_found (no
// such order in the scope) or invalid (not a counter order).
func (r *OrderRepository) AddCounterPayment(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, orderID uuid.UUID, p order.CheckedPayment) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := models.New(tx)

	var source string
	err = tx.QueryRow(ctx, `SELECT source FROM orders WHERE id = $1 AND tenant_id = $2 AND location_id = $3 FOR UPDATE`,
		orderID, uuid.UUID(sc.TenantID), uuid.UUID(sc.LocationID)).Scan(&source)
	result := order.FloorApplied
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		result = order.FloorNotFound
	case err != nil:
		return "", fmt.Errorf("get order: %w", err)
	case source != string(order.SourceCounter):
		result = order.FloorInvalid
	default:
		added, err := insertCounterPayment(ctx, tx, orderID, p)
		if err != nil {
			return "", err
		}
		if !added {
			result = order.FloorDuplicate
		}
	}
	var oid *uuid.UUID
	if result != order.FloorNotFound {
		oid = &orderID
	}
	fresh, err := markSeen(ctx, q, sc, eventID, oid, typ, result)
	if err != nil {
		return "", err
	}
	if !fresh {
		return order.FloorDuplicate, nil
	}
	if result == order.FloorApplied {
		if _, err := tx.Exec(ctx, `UPDATE orders SET updated_at = now() WHERE id = $1`, orderID); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return result, nil
}

// ChangeCounterOrder replaces a counter order's line items and money with
// o's (the Pi's order.changed, a kitchen change after SEND): applied,
// duplicate (the event was seen), not_found (no such order in the scope) or
// invalid (not a counter order, or cancelled). Payments, status and the
// number are left as they are.
func (r *OrderRepository) ChangeCounterOrder(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, orderID uuid.UUID, o *order.Order) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := models.New(tx)

	var source, status string
	err = tx.QueryRow(ctx, `SELECT source, status FROM orders WHERE id = $1 AND tenant_id = $2 AND location_id = $3 FOR UPDATE`,
		orderID, uuid.UUID(sc.TenantID), uuid.UUID(sc.LocationID)).Scan(&source, &status)
	result := order.FloorApplied
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		result = order.FloorNotFound
	case err != nil:
		return "", fmt.Errorf("get order: %w", err)
	case source != string(order.SourceCounter), status == string(order.StatusCancelled):
		result = order.FloorInvalid
	}
	var oid *uuid.UUID
	if result != order.FloorNotFound {
		oid = &orderID
	}
	fresh, err := markSeen(ctx, q, sc, eventID, oid, typ, result)
	if err != nil {
		return "", err
	}
	if !fresh {
		return order.FloorDuplicate, nil
	}
	if result == order.FloorApplied {
		if _, err := tx.Exec(ctx, `DELETE FROM line_items WHERE order_id = $1`, orderID); err != nil {
			return "", fmt.Errorf("drop line items: %w", err)
		}
		for _, item := range o.Items() {
			if err := insertLineItemTree(ctx, q, order.ID(orderID), nil, item); err != nil {
				return "", fmt.Errorf("line items: %w", err)
			}
		}
		m := o.Money()
		fees, _ := json.Marshal(m.Fees)
		discounts, _ := json.Marshal(m.Discounts)
		if _, err := tx.Exec(ctx, `UPDATE orders SET subtotal_cents = $2, tax_cents = $3, tip_cents = $4, total_cents = $5,
			fees = $6, discounts = $7, updated_at = now() WHERE id = $1`,
			orderID, int64(m.Subtotal), int64(m.Tax), int64(m.Tip), int64(m.Total), fees, discounts); err != nil {
			return "", fmt.Errorf("update money: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return result, nil
}
