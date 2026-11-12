package repositories

import (
	"context"
	"errors"
	"fmt"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AddGeneralRefund stores the Pi's refund.general (agent contract 2.10x):
// cash a terminal gave back with no order, as an unlinked counter refund
// (payments: order_id NULL, origin counter, status refunded, the store and
// the Pi's batch, the reason), in one transaction with its
// floor_events_seen row. Idempotent like order.payment: the same id /
// client_key with the same body = duplicate, with another body = conflict
// (kept in money_flags, nothing inserted). Flags (stored all the same):
// over_staff_limit when nobody approved more than the refunder's limit. A
// batch whose Z is in already is checked again (M-12).
func (r *OrderRepository) AddGeneralRefund(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, p order.CheckedPayment,
	reason string) (string, []order.MoneyFlag, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	result, flags, err := storeGeneralRefund(ctx, tx, sc, eventID, typ, p, reason)
	if err != nil {
		return "", nil, err
	}
	fresh, err := markSeen(ctx, models.New(tx), sc, eventID, nil, typ, result)
	if err != nil {
		return "", nil, err
	}
	if !fresh {
		return order.FloorDuplicate, nil, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return "", nil, fmt.Errorf("commit: %w", err)
	}
	return result, flags, nil
}

func storeGeneralRefund(ctx context.Context, tx pgx.Tx, sc order.Scope, eventID uuid.UUID, typ string, p order.CheckedPayment,
	reason string) (string, []order.MoneyFlag, error) {
	loc := uuid.UUID(sc.LocationID)
	conflict := func(why string, other uuid.UUID) (string, []order.MoneyFlag, error) {
		d := paymentBody(p)
		d["why"], d["other_payment_id"], d["general"] = why, other.String(), true
		f := []order.MoneyFlag{{Code: order.FlagPaymentConflict, Detail: d}}
		return order.FloorConflict, f, insertMoneyFlags(ctx, tx, sc, eventID, typ, "refund:"+p.ID.String(), f)
	}
	same := func(oid *uuid.UUID, method string, amount, tip int64, exLoc *uuid.UUID) bool {
		return oid == nil && method == p.Method && amount == int64(p.Amount) && tip == int64(p.Tip) && exLoc != nil && *exLoc == loc
	}
	var exOrder, exLoc *uuid.UUID
	var exMethod string
	var exAmount, exTip int64
	err := tx.QueryRow(ctx, `SELECT order_id, processor, amount_cents, tip_cents, location_id FROM payments WHERE id = $1`, p.ID).
		Scan(&exOrder, &exMethod, &exAmount, &exTip, &exLoc)
	switch {
	case err == nil && same(exOrder, exMethod, exAmount, exTip, exLoc):
		return order.FloorDuplicate, nil, nil
	case err == nil:
		return conflict("payment id holds another payment", p.ID)
	case !errors.Is(err, pgx.ErrNoRows):
		return "", nil, fmt.Errorf("payment by id: %w", err)
	}
	if p.ClientKey != "" {
		var exID uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id, order_id, processor, amount_cents, tip_cents, location_id FROM payments
			WHERE location_id = $1 AND client_key = $2`, loc, p.ClientKey).Scan(&exID, &exOrder, &exMethod, &exAmount, &exTip, &exLoc)
		switch {
		case err == nil && same(exOrder, exMethod, exAmount, exTip, exLoc):
			return order.FloorDuplicate, nil, nil
		case err == nil:
			return conflict("client_key used by another payment", exID)
		case !errors.Is(err, pgx.ErrNoRows):
			return "", nil, fmt.Errorf("payment by key: %w", err)
		}
	}
	flags := append([]order.MoneyFlag(nil), p.Flags...)
	if p.ApprovedBy == "" && p.RefundLimit != nil && -int64(p.Amount) > *p.RefundLimit {
		flags = append(flags, order.MoneyFlag{Code: order.FlagOverStaffLimit, Detail: map[string]any{"limit_cents": *p.RefundLimit,
			"refunded_cents": -int64(p.Amount), "general": true}})
	}
	if _, err := tx.Exec(ctx, `INSERT INTO payments (id, order_id, processor, status, amount_cents, token, last4, captured_at, auth_code,
			tip_cents, staff_id, location_id, client_key, origin, batch_id, approved_by, flags, reason)
		VALUES ($1, NULL, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 'counter', $13, $14, $15, $16)`,
		p.ID, p.Method, string(order.PaymentRefunded), int64(p.Amount), p.Ref, p.Last4, p.At, p.AuthCode, int64(p.Tip), p.StaffID,
		loc, p.ClientKey, p.BatchID, p.ApprovedBy, flagCodes(flags), reason); err != nil {
		return "", nil, fmt.Errorf("insert general refund: %w", err)
	}
	if err := insertMoneyFlags(ctx, tx, sc, eventID, typ, "refund:"+p.ID.String(), flags); err != nil {
		return "", nil, err
	}
	if p.BatchID != nil {
		if err := RecheckZ(ctx, tx, sc, eventID, typ, *p.BatchID); err != nil {
			return "", nil, fmt.Errorf("z recheck: %w", err)
		}
	}
	return order.FloorApplied, flags, nil
}
