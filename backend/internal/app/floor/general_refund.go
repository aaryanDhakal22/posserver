package floor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/domain/staff"

	"github.com/google/uuid"
)

// EventGeneralRefund is the Pi's refund.general (agent contract 2.10x,
// contract/main.md §2.5zb): cash a terminal gave back with no order.
const EventGeneralRefund = "refund.general"

// GeneralRefunds stores general refunds (repositories.OrderRepository).
type GeneralRefunds interface {
	AddGeneralRefund(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, p order.CheckedPayment, reason string) (string, []order.MoneyFlag, error)
}

// applyGeneralRefund: {payment (a negative amount, method cash), reason,
// batch_id} → an unlinked counter refund. Stored and flagged, never refused
// for its money (M-2); invalid only when it can't be stored (not a refund,
// a bad id, an unknown method).
func (s *Service) applyGeneralRefund(ctx context.Context, sc order.Scope, eid uuid.UUID, ev event) (string, error) {
	repo, ok := s.counter.(GeneralRefunds)
	if s.counter == nil || !ok || len(ev.Payment) == 0 {
		return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorInvalid)
	}
	var p order.CounterPayment
	if err := json.Unmarshal(ev.Payment, &p); err != nil {
		return s.invalid(ctx, sc, eid, ev, err)
	}
	cp, err := p.Check()
	if err != nil {
		return s.invalid(ctx, sc, eid, ev, err)
	}
	if cp.Amount >= 0 {
		return s.invalid(ctx, sc, eid, ev, errors.New("a general refund is a negative amount"))
	}
	if cp.BatchID == nil {
		cp.BatchID = ev.BatchID
	}
	reason := strings.TrimSpace(ev.Reason)
	if reason == "" {
		reason = "no reason given"
	}
	if utf8.RuneCountInString(reason) > staff.MaxReasonLen {
		reason = string([]rune(reason)[:staff.MaxReasonLen])
	}
	s.refundLimit(ctx, sc, &cp)
	res, flags, err := repo.AddGeneralRefund(ctx, sc, eid, ev.Type, cp, reason)
	if err != nil {
		return "", err
	}
	switch res {
	case order.FloorApplied:
		e := s.logger.Warn().Ctx(ctx).Str("payment_id", cp.ID.String()).Str("method", cp.Method).Int64("amount_cents", int64(cp.Amount)).
			Str("approved_by", cp.ApprovedBy).Str("reason", reason)
		if cp.StaffID != nil {
			e = e.Str("staff_id", cp.StaffID.String())
		}
		e.Msg("general refund recorded")
		s.meters.RecordCounterPayment(ctx, uuid.UUID(sc.LocationID), cp.Method, int64(cp.Amount), int64(cp.Tip))
		s.flagged(ctx, sc, eid, ev.Type, "refund:"+cp.ID.String(), flags)
	case order.FloorConflict:
		s.flagged(ctx, sc, eid, ev.Type, "refund:"+cp.ID.String(), flags)
	}
	return res, nil
}
