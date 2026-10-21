package repositories

import (
	"context"
	"fmt"
	"time"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

var _ order.FloorRepository = (*OrderRepository)(nil)

// ApplyFloorUpdate — see order.FloorRepository. The order row is locked for
// the whole load → apply → write, so two floor POSTs for one order (an agent
// retry overlapping the original) apply one after the other instead of the
// second one writing back a stale status.
func (r *OrderRepository) ApplyFloorUpdate(ctx context.Context, scope order.Scope, id order.ID, eventID uuid.UUID, apply func(o *order.Order) string) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := models.New(tx)

	row, err := q.GetOrderInScopeForUpdate(ctx, models.GetOrderInScopeForUpdateParams{
		ID:         toPgUUID(uuid.UUID(id)),
		TenantID:   toPgUUID(uuid.UUID(scope.TenantID)),
		LocationID: toPgUUID(uuid.UUID(scope.LocationID)),
	})
	if err != nil {
		return "", fmt.Errorf("get order: %w", err) // pgx.ErrNoRows → caller maps to not_found
	}
	o, err := r.assemble(ctx, q, models.Order(row))
	if err != nil {
		return "", err
	}
	prev := len(o.StatusHistory())
	result := apply(o)

	oid := uuid.UUID(o.ID())
	n, err := q.MarkFloorEventSeen(ctx, models.MarkFloorEventSeenParams{
		EventID:    toPgUUID(eventID),
		TenantID:   toPgUUID(uuid.UUID(o.TenantID())),
		LocationID: toPgUUID(uuid.UUID(o.LocationID())),
		OrderID:    toPgUUID(oid),
		Type:       "order.status",
		Result:     result,
	})
	if err != nil {
		return "", fmt.Errorf("mark floor event: %w", err)
	}
	if n == 0 {
		return order.FloorDuplicate, nil
	}
	if result == order.FloorApplied {
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
			ID:                 toPgUUID(oid),
			Status:             string(o.Status()),
			KitchenOrderNumber: kitchen,
			EstimatedReadyAt:   eta,
			DriverName:         o.DriverName(),
			UpdatedAt:          pgtype.Timestamptz{Time: o.UpdatedAt(), Valid: true},
			FloorAt:            floorAt,
		}); err != nil {
			return "", fmt.Errorf("update order floor: %w", err)
		}
		hist := o.StatusHistory()
		for i := prev; i < len(hist); i++ {
			if err := q.CreateStatusEvent(ctx, toStatusEventRow(o.ID(), i, hist[i])); err != nil {
				return "", fmt.Errorf("create status event: %w", err)
			}
		}
		for _, ev := range o.PullEvents() {
			if err := insertOutboxEvent(ctx, q, o.TenantID(), o.LocationID(), ev, r.handlerNames); err != nil {
				return "", fmt.Errorf("emit %s: %w", ev.Type, err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return result, nil
}

// MarkFloorEventSeen — see order.FloorRepository.
func (r *OrderRepository) MarkFloorEventSeen(ctx context.Context, scope order.Scope, eventID uuid.UUID, orderID *uuid.UUID, typ, result string) (bool, error) {
	n, err := models.New(r.pool).MarkFloorEventSeen(ctx, models.MarkFloorEventSeenParams{
		EventID:    toPgUUID(eventID),
		TenantID:   toPgUUID(uuid.UUID(scope.TenantID)),
		LocationID: toPgUUID(uuid.UUID(scope.LocationID)),
		OrderID:    optPgUUID(orderID),
		Type:       typ,
		Result:     result,
	})
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

// ReplaceDrivers — see order.FloorRepository.
func (r *OrderRepository) ReplaceDrivers(ctx context.Context, scope order.Scope, eventID uuid.UUID, drivers []order.Driver) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := models.New(tx)
	tid, lid := toPgUUID(uuid.UUID(scope.TenantID)), toPgUUID(uuid.UUID(scope.LocationID))

	n, err := q.MarkFloorEventSeen(ctx, models.MarkFloorEventSeenParams{
		EventID: toPgUUID(eventID), TenantID: tid, LocationID: lid, Type: "drivers.roster", Result: order.FloorApplied,
	})
	if err != nil {
		return "", err
	}
	if n == 0 {
		return order.FloorDuplicate, nil
	}
	if err := q.DeleteLocationDrivers(ctx, models.DeleteLocationDriversParams{TenantID: tid, LocationID: lid}); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	for _, d := range drivers {
		if err := q.InsertDriver(ctx, models.InsertDriverParams{
			ID: toPgUUID(d.ID), TenantID: tid, LocationID: lid, Name: d.Name,
			Position: int32(d.Position), Status: d.Status, UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true},
		}); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return order.FloorApplied, nil
}

// SaveDriverShift — see order.FloorRepository.
func (r *OrderRepository) SaveDriverShift(ctx context.Context, scope order.Scope, eventID uuid.UUID, sh order.DriverShift) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := models.New(tx)
	tid, lid := toPgUUID(uuid.UUID(scope.TenantID)), toPgUUID(uuid.UUID(scope.LocationID))
	n, err := q.MarkFloorEventSeen(ctx, models.MarkFloorEventSeenParams{
		EventID: toPgUUID(eventID), TenantID: tid, LocationID: lid, Type: "driver.shift", Result: order.FloorApplied,
	})
	if err != nil {
		return "", err
	}
	if n == 0 {
		return order.FloorDuplicate, nil
	}
	var out pgtype.Timestamptz
	if sh.ClockOut != nil {
		out = pgtype.Timestamptz{Time: *sh.ClockOut, Valid: true}
	}
	if err := q.UpsertDriverShift(ctx, models.UpsertDriverShiftParams{
		ID: toPgUUID(sh.ID), TenantID: tid, LocationID: lid, DriverID: toPgUUID(sh.DriverID), DriverName: sh.DriverName,
		ClockIn: pgtype.Timestamptz{Time: sh.ClockIn, Valid: true}, ClockOut: out, AutoOut: sh.AutoOut,
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return order.FloorApplied, nil
}

// ListDriverShifts — see order.FloorRepository.
func (r *OrderRepository) ListDriverShifts(ctx context.Context, scope order.Scope, since time.Time) ([]order.DriverShift, error) {
	rows, err := models.New(r.pool).ListDriverShifts(ctx, models.ListDriverShiftsParams{
		TenantID: toPgUUID(uuid.UUID(scope.TenantID)), LocationID: toPgUUID(uuid.UUID(scope.LocationID)),
		ClockIn: pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	out := make([]order.DriverShift, 0, len(rows))
	for _, sh := range rows {
		ds := order.DriverShift{ID: uuid.UUID(sh.ID.Bytes), DriverID: uuid.UUID(sh.DriverID.Bytes), DriverName: sh.DriverName,
			ClockIn: sh.ClockIn.Time, AutoOut: sh.AutoOut}
		if sh.ClockOut.Valid {
			t := sh.ClockOut.Time
			ds.ClockOut = &t
		}
		out = append(out, ds)
	}
	return out, nil
}

// ListDrivers — see order.FloorRepository.
func (r *OrderRepository) ListDrivers(ctx context.Context, scope order.Scope) ([]order.Driver, error) {
	rows, err := models.New(r.pool).ListLocationDrivers(ctx, models.ListLocationDriversParams{
		TenantID: toPgUUID(uuid.UUID(scope.TenantID)), LocationID: toPgUUID(uuid.UUID(scope.LocationID)),
	})
	if err != nil {
		return nil, err
	}
	out := make([]order.Driver, 0, len(rows))
	for _, d := range rows {
		out = append(out, order.Driver{ID: uuid.UUID(d.ID.Bytes), Name: d.Name, Position: int(d.Position),
			Status: d.Status, UpdatedAt: d.UpdatedAt.Time})
	}
	return out, nil
}

// LastFloorEventAt — see order.FloorRepository.
func (r *OrderRepository) LastFloorEventAt(ctx context.Context, scope order.Scope) (*time.Time, error) {
	t, err := models.New(r.pool).LastFloorEventAt(ctx, models.LastFloorEventAtParams{
		TenantID: toPgUUID(uuid.UUID(scope.TenantID)), LocationID: toPgUUID(uuid.UUID(scope.LocationID)),
	})
	if err != nil {
		return nil, err
	}
	if !t.Valid {
		return nil, nil
	}
	return &t.Time, nil
}

// SetAgentStatus — see order.FloorRepository.
func (r *OrderRepository) SetAgentStatus(ctx context.Context, scope order.Scope, id uuid.UUID, status []byte, batchID *int64, at time.Time) error {
	batch := pgtype.Int8{}
	if batchID != nil {
		batch = pgtype.Int8{Int64: *batchID, Valid: true}
	}
	_, err := models.New(r.pool).SetOrderAgentStatus(ctx, models.SetOrderAgentStatusParams{
		AgentStatus: status,
		At:          pgtype.Timestamptz{Time: at, Valid: true},
		BatchID:     batch,
		ID:          toPgUUID(id),
		TenantID:    toPgUUID(uuid.UUID(scope.TenantID)),
		LocationID:  toPgUUID(uuid.UUID(scope.LocationID)),
	})
	return err
}
