package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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

// insertMoneyFlags writes one money_flags row per flag (in the event's tx).
func insertMoneyFlags(ctx context.Context, tx pgx.Tx, sc order.Scope, eventID uuid.UUID, typ, subject string, flags []order.MoneyFlag) error {
	for _, f := range flags {
		detail, _ := json.Marshal(f.Detail)
		if f.Detail == nil {
			detail = []byte("{}")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO money_flags (tenant_id, location_id, event_id, event_type, subject, code, detail)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, uuid.UUID(sc.TenantID), uuid.UUID(sc.LocationID), eventID, typ, subject, f.Code, detail); err != nil {
			return fmt.Errorf("money flag: %w", err)
		}
	}
	return nil
}

func flagCodes(flags []order.MoneyFlag) []string {
	out := make([]string, 0, len(flags))
	for _, f := range flags {
		out = append(out, f.Code)
	}
	return out
}

// payingOrder is what a payment is checked against (the order row, locked).
type payingOrder struct {
	status string
	total  int64
	batch  *int64
}

func paymentBody(p order.CheckedPayment) map[string]any {
	return map[string]any{"payment_id": p.ID.String(), "method": p.Method, "amount_cents": int64(p.Amount), "tip_cents": int64(p.Tip),
		"ref": p.Ref, "client_key": p.ClientKey, "at": p.At}
}

// storeCounterPayment stores one counter payment on a locked order (M-7):
// duplicate when the id / client_key holds the same payment, conflict when it
// holds another (kept whole in money_flags, nothing inserted), else inserted
// with whatever main disagrees with as flags (never refused: the drawer has
// moved already).
func storeCounterPayment(ctx context.Context, tx pgx.Tx, sc order.Scope, eventID uuid.UUID, typ string, orderID uuid.UUID,
	o payingOrder, p order.CheckedPayment) (string, []order.MoneyFlag, error) {
	conflict := func(why string, other uuid.UUID) (string, []order.MoneyFlag, error) {
		d := paymentBody(p)
		d["order_id"], d["why"], d["other_payment_id"] = orderID.String(), why, other.String()
		f := []order.MoneyFlag{{Code: order.FlagPaymentConflict, Detail: d}}
		return order.FloorConflict, f, insertMoneyFlags(ctx, tx, sc, eventID, typ, "payment:"+p.ID.String(), f)
	}
	same := func(id, oid uuid.UUID, method string, amount, tip int64, ref string) bool {
		return oid == orderID && method == p.Method && amount == int64(p.Amount) && tip == int64(p.Tip) && ref == p.Ref
	}
	var exOrder uuid.UUID
	var exMethod, exRef string
	var exAmount, exTip int64
	err := tx.QueryRow(ctx, `SELECT order_id, processor, amount_cents, tip_cents, token FROM payments WHERE id = $1`, p.ID).
		Scan(&exOrder, &exMethod, &exAmount, &exTip, &exRef)
	switch {
	case err == nil && same(p.ID, exOrder, exMethod, exAmount, exTip, exRef):
		return order.FloorDuplicate, nil, nil
	case err == nil:
		return conflict("payment id holds another payment", p.ID)
	case !errors.Is(err, pgx.ErrNoRows):
		return "", nil, fmt.Errorf("payment by id: %w", err)
	}
	if p.ClientKey != "" {
		var exID uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id, order_id, processor, amount_cents, tip_cents, token FROM payments
			WHERE location_id = $1 AND client_key = $2`, uuid.UUID(sc.LocationID), p.ClientKey).
			Scan(&exID, &exOrder, &exMethod, &exAmount, &exTip, &exRef)
		switch {
		case err == nil && same(exID, exOrder, exMethod, exAmount, exTip, exRef):
			return order.FloorDuplicate, nil, nil
		case err == nil:
			return conflict("client_key used by another payment", exID)
		case !errors.Is(err, pgx.ErrNoRows):
			return "", nil, fmt.Errorf("payment by key: %w", err)
		}
	}
	if p.Ref != "" && p.Amount > 0 {
		var exID uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM payments WHERE location_id = $1 AND processor = $2 AND token = $3
			AND origin = 'counter' AND amount_cents > 0 LIMIT 1`, uuid.UUID(sc.LocationID), p.Method, p.Ref).Scan(&exID)
		switch {
		case err == nil:
			return conflict("processor reference recorded under another payment id", exID)
		case !errors.Is(err, pgx.ErrNoRows):
			return "", nil, fmt.Errorf("payment by ref: %w", err)
		}
	}

	// What main checks (it flags, it doesn't refuse).
	flags := append([]order.MoneyFlag(nil), p.Flags...)
	var paid, tender, unapproved int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(amount_cents), 0),
			COALESCE(sum(amount_cents + tip_cents) FILTER (WHERE processor = $2), 0),
			COALESCE(sum(-amount_cents) FILTER (WHERE amount_cents < 0 AND approved_by = ''), 0)
		FROM payments WHERE order_id = $1 AND status IN ('captured', 'refunded')`, orderID, p.Method).
		Scan(&paid, &tender, &unapproved); err != nil {
		return "", nil, fmt.Errorf("order payments: %w", err)
	}
	amount, tip := int64(p.Amount), int64(p.Tip)
	if amount > 0 && o.status == string(order.StatusCancelled) {
		flags = append(flags, order.MoneyFlag{Code: order.FlagOrderCancelled})
	}
	if amount > 0 && paid+amount > o.total {
		flags = append(flags, order.MoneyFlag{Code: order.FlagOverpaid, Detail: map[string]any{"paid_cents": paid + amount, "total_cents": o.total}})
	}
	var refundOf *uuid.UUID
	if amount < 0 {
		if paid+amount < 0 {
			flags = append(flags, order.MoneyFlag{Code: order.FlagRefundExceedsPaid, Detail: map[string]any{"paid_cents": paid, "refund_cents": -amount}})
		} else if tender+amount+tip < 0 && p.ApprovedBy == "" {
			flags = append(flags, order.MoneyFlag{Code: order.FlagRefundExceedsTender, Detail: map[string]any{"tender": p.Method,
				"tender_net_cents": tender, "refund_cents": -amount}})
		}
		if p.ApprovedBy == "" && p.RefundLimit != nil && unapproved-amount > *p.RefundLimit {
			flags = append(flags, order.MoneyFlag{Code: order.FlagOverStaffLimit, Detail: map[string]any{"limit_cents": *p.RefundLimit,
				"refunded_cents": unapproved - amount}})
		}
		if p.RefundOf != nil {
			var orig, before int64
			var oid uuid.UUID
			err := tx.QueryRow(ctx, `SELECT order_id, amount_cents + tip_cents,
					(SELECT COALESCE(sum(-amount_cents), 0) FROM payments r WHERE r.refund_of = p.id)
				FROM payments p WHERE p.id = $1`, *p.RefundOf).Scan(&oid, &orig, &before)
			switch {
			case errors.Is(err, pgx.ErrNoRows) || (err == nil && oid != orderID):
				flags = append(flags, order.MoneyFlag{Code: order.FlagRefundOfUnknown, Detail: map[string]any{"refund_of": p.RefundOf.String()}})
			case err != nil:
				return "", nil, fmt.Errorf("refunded payment: %w", err)
			default:
				refundOf = p.RefundOf
				if before-amount > orig {
					flags = append(flags, order.MoneyFlag{Code: order.FlagRefundExceedsOrig, Detail: map[string]any{
						"original_cents": orig, "refunded_cents": before - amount}})
				}
			}
		}
	}
	batch := p.BatchID
	if batch == nil {
		batch = o.batch
	}
	if _, err := tx.Exec(ctx, `INSERT INTO payments (id, order_id, processor, status, amount_cents, token, last4, captured_at, auth_code,
			tip_cents, staff_id, location_id, client_key, origin, batch_id, approved_by, refund_of, flags)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'counter', $14, $15, $16, $17)`,
		p.ID, orderID, p.Method, string(p.Status), amount, p.Ref, p.Last4, p.At, p.AuthCode, tip, p.StaffID,
		uuid.UUID(sc.LocationID), p.ClientKey, batch, p.ApprovedBy, refundOf, flagCodes(flags)); err != nil {
		return "", nil, fmt.Errorf("insert payment: %w", err)
	}
	if err := insertMoneyFlags(ctx, tx, sc, eventID, typ, "payment:"+p.ID.String(), flags); err != nil {
		return "", nil, err
	}
	if batch != nil {
		// A payment for a batch whose Z is in already: main's check again (M-12).
		if err := RecheckZ(ctx, tx, sc, eventID, typ, *batch); err != nil {
			return "", nil, fmt.Errorf("z recheck: %w", err)
		}
	}
	return order.FloorApplied, flags, nil
}

// CreateCounterOrder stores the order the Pi placed: applied, or duplicate
// (the event was seen, or the order — by id or by its source_event_id — is
// there already). The order is saved acked (never re-sent to the Pi) with
// the Pi's floor state; agentStatus / batchID are the Pi's restore data.
func (r *OrderRepository) CreateCounterOrder(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, co *order.CounterOrder,
	agentStatus []byte, batchID *int64) (string, []order.MoneyFlag, error) {
	res, flags, err := r.createCounterOrder(ctx, sc, eventID, typ, co, agentStatus, batchID)
	return res, flags, err
}

func (r *OrderRepository) createCounterOrder(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, co *order.CounterOrder,
	agentStatus []byte, batchID *int64) (string, []order.MoneyFlag, error) {
	o := co.Order
	oid := uuid.UUID(o.ID())
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := models.New(tx)

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM orders WHERE id = $1
		OR (tenant_id = $2 AND source = $3 AND source_event_id = $4))`,
		oid, uuid.UUID(sc.TenantID), string(order.SourceCounter), o.SourceEventID()).Scan(&exists); err != nil {
		return "", nil, fmt.Errorf("order exists: %w", err)
	}
	result := order.FloorApplied
	if exists {
		result = order.FloorDuplicate
	}
	fresh, err := markSeen(ctx, q, sc, eventID, &oid, typ, result)
	if err != nil {
		return "", nil, err
	}
	if !fresh {
		return order.FloorDuplicate, nil, nil
	}
	if exists {
		return order.FloorDuplicate, nil, tx.Commit(ctx)
	}

	// The Pi's floor state (accepted + the counter number by default).
	o.ApplyFloorUpdate(co.Floor)
	if err := r.createInTx(ctx, q, o); err != nil {
		return "", nil, err
	}
	flags := append([]order.MoneyFlag(nil), co.Flags...)
	if err := insertMoneyFlags(ctx, tx, sc, eventID, typ, "order:"+oid.String(), co.Flags); err != nil {
		return "", nil, err
	}
	paying := payingOrder{status: string(o.Status()), total: int64(o.Money().Total), batch: batchID}
	for _, p := range co.Payments {
		_, pf, err := storeCounterPayment(ctx, tx, sc, eventID, typ, oid, paying, p)
		if err != nil {
			return "", nil, err
		}
		flags = append(flags, pf...)
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
		return "", nil, fmt.Errorf("update order floor: %w", err)
	}
	// The Pi made it: never re-sent (RunRedeliver only looks at unacked rows).
	if _, err := tx.Exec(ctx, `UPDATE orders SET agent_ack_at = now() WHERE id = $1`, oid); err != nil {
		return "", nil, fmt.Errorf("ack: %w", err)
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
			return "", nil, fmt.Errorf("agent status: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", nil, fmt.Errorf("commit: %w", err)
	}
	return order.FloorApplied, flags, nil
}

// AddCounterPayment adds a payment (or refund) the Pi's terminal took to an
// order of the store, any source (a counter order, or an online / website
// "pay at pickup" order paid at the counter): applied (with whatever main
// flagged), duplicate (the event was seen, or the payment id / client_key
// holds this same payment), conflict (they hold another payment: kept in
// money_flags) or not_found (no such order in the scope). Only the order's
// payments move; its snapshot stays as it was. A carried-over order (open
// across batches) takes payments like any other.
func (r *OrderRepository) AddCounterPayment(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, orderID uuid.UUID,
	p order.CheckedPayment) (string, []order.MoneyFlag, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := models.New(tx)

	var o payingOrder
	err = tx.QueryRow(ctx, `SELECT status, total_cents, agent_batch_id FROM orders WHERE id = $1 AND tenant_id = $2 AND location_id = $3 FOR UPDATE`,
		orderID, uuid.UUID(sc.TenantID), uuid.UUID(sc.LocationID)).Scan(&o.status, &o.total, &o.batch)
	result := order.FloorApplied
	var flags []order.MoneyFlag
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		result = order.FloorNotFound
	case err != nil:
		return "", nil, fmt.Errorf("get order: %w", err)
	default:
		result, flags, err = storeCounterPayment(ctx, tx, sc, eventID, typ, orderID, o, p)
		if err != nil {
			return "", nil, err
		}
	}
	var oid *uuid.UUID
	if result != order.FloorNotFound {
		oid = &orderID
	}
	fresh, err := markSeen(ctx, q, sc, eventID, oid, typ, result)
	if err != nil {
		return "", nil, err
	}
	if !fresh {
		return order.FloorDuplicate, nil, nil
	}
	if result == order.FloorApplied {
		if _, err := tx.Exec(ctx, `UPDATE orders SET updated_at = now() WHERE id = $1`, orderID); err != nil {
			return "", nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", nil, fmt.Errorf("commit: %w", err)
	}
	return result, flags, nil
}

// ChangeCounterOrder replaces a counter order's line items, money and
// fulfillment (type + address: a takeout moved to a delivery or back) with
// o's (the Pi's order.changed, a kitchen change after SEND): applied,
// duplicate (the event was seen), stale (a newer change is applied already:
// at is older than orders.money_changed_at, M-12), not_found (no such order
// in the scope) or invalid (not a counter order, or cancelled). Payments,
// status and the number are left as they are. Every applied change keeps
// its before / after in order_money_changes; a change after its batch's Z is
// applied and flagged (a carried-over order may change in a later batch).
func (r *OrderRepository) ChangeCounterOrder(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, orderID uuid.UUID,
	o *order.Order, at time.Time, flags []order.MoneyFlag) (string, []order.MoneyFlag, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := models.New(tx)

	var source, status string
	var changedAt *time.Time
	var batch *int64
	var before []byte
	err = tx.QueryRow(ctx, `SELECT source, status, money_changed_at, agent_batch_id,
			jsonb_build_object('subtotal_cents', subtotal_cents, 'tax_cents', tax_cents, 'tip_cents', tip_cents,
				'total_cents', total_cents, 'fees', fees, 'discounts', discounts,
				'lines', (SELECT COALESCE(jsonb_agg(jsonb_build_object('name', name, 'quantity', quantity, 'line_total_cents', line_total_cents)
					ORDER BY position), '[]'::jsonb) FROM line_items WHERE order_id = orders.id AND parent_id IS NULL))
		FROM orders WHERE id = $1 AND tenant_id = $2 AND location_id = $3 FOR UPDATE`,
		orderID, uuid.UUID(sc.TenantID), uuid.UUID(sc.LocationID)).Scan(&source, &status, &changedAt, &batch, &before)
	result := order.FloorApplied
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		result = order.FloorNotFound
	case err != nil:
		return "", nil, fmt.Errorf("get order: %w", err)
	case source != string(order.SourceCounter), status == string(order.StatusCancelled):
		result = order.FloorInvalid
	case changedAt != nil && !at.IsZero() && at.Before(*changedAt):
		result = order.FloorStale // an older change after a newer one: the newer one stands
	}
	var oid *uuid.UUID
	if result != order.FloorNotFound {
		oid = &orderID
	}
	fresh, err := markSeen(ctx, q, sc, eventID, oid, typ, result)
	if err != nil {
		return "", nil, err
	}
	if !fresh {
		return order.FloorDuplicate, nil, nil
	}
	if result == order.FloorApplied {
		if _, err := tx.Exec(ctx, `DELETE FROM line_items WHERE order_id = $1`, orderID); err != nil {
			return "", nil, fmt.Errorf("drop line items: %w", err)
		}
		for _, item := range o.Items() {
			if err := insertLineItemTree(ctx, q, order.ID(orderID), nil, item); err != nil {
				return "", nil, fmt.Errorf("line items: %w", err)
			}
		}
		m := o.Money()
		fees, _ := json.Marshal(m.Fees)
		discounts, _ := json.Marshal(m.Discounts)
		if at.IsZero() {
			at = time.Now()
		}
		// The fulfillment follows too (agent §2.10d): a takeout moved to a delivery (its address) or back.
		var addr []byte
		if a := o.Fulfillment().Address; a != nil {
			addr, _ = json.Marshal(a)
		}
		if _, err := tx.Exec(ctx, `UPDATE orders SET subtotal_cents = $2, tax_cents = $3, tip_cents = $4, total_cents = $5,
			fees = $6, discounts = $7, money_changed_at = $8, fulfillment_type = $9, fulfillment_address = $10, updated_at = now()
			WHERE id = $1`,
			orderID, int64(m.Subtotal), int64(m.Tax), int64(m.Tip), int64(m.Total), fees, discounts, at.UTC(),
			string(o.Fulfillment().Type), addr); err != nil {
			return "", nil, fmt.Errorf("update money: %w", err)
		}
		afterZ := false
		if batch != nil {
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM z_reports WHERE location_id = $1 AND batch_id = $2)`,
				uuid.UUID(sc.LocationID), *batch).Scan(&afterZ); err != nil {
				return "", nil, fmt.Errorf("z for batch: %w", err)
			}
		}
		if afterZ {
			flags = append(flags, order.MoneyFlag{Code: order.FlagChangedAfterZ, Detail: map[string]any{"batch_id": *batch}})
		}
		after, _ := json.Marshal(map[string]any{"subtotal_cents": int64(m.Subtotal), "tax_cents": int64(m.Tax), "tip_cents": int64(m.Tip),
			"total_cents": int64(m.Total), "fees": m.Fees, "discounts": m.Discounts, "lines": len(o.Items())})
		if _, err := tx.Exec(ctx, `INSERT INTO order_money_changes (order_id, tenant_id, location_id, event_id, at, before, after, after_z)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, orderID, uuid.UUID(sc.TenantID), uuid.UUID(sc.LocationID), eventID, at.UTC(),
			before, after, afterZ); err != nil {
			return "", nil, fmt.Errorf("money change history: %w", err)
		}
		if err := insertMoneyFlags(ctx, tx, sc, eventID, typ, "order:"+orderID.String(), flags); err != nil {
			return "", nil, err
		}
	} else {
		flags = nil
	}
	if err := tx.Commit(ctx); err != nil {
		return "", nil, fmt.Errorf("commit: %w", err)
	}
	return result, flags, nil
}
