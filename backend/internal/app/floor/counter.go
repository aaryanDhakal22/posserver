package floor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/domain/staff"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Counter orders from the Pi (TERMINAL-PLAN.md 0d, contract/main.md §2.5zb):
// order.placed, order.payment, order.void, order.changed and journal.entries.

func (s *Service) applyPlaced(ctx context.Context, sc order.Scope, eid uuid.UUID, ev event) (string, error) {
	if s.counter == nil || len(ev.Snapshot) == 0 {
		return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorInvalid)
	}
	var snap order.CounterSnapshot
	if err := json.Unmarshal(ev.Snapshot, &snap); err != nil {
		return s.invalid(ctx, sc, eid, ev, err)
	}
	if ev.OrderID != "" && ev.OrderID != snap.ID {
		return s.invalid(ctx, sc, eid, ev, errors.New("order_id differs from the snapshot's id"))
	}
	co, err := order.NewCounterOrder(sc, snap, ev.Snapshot)
	if err != nil {
		return s.invalid(ctx, sc, eid, ev, err)
	}
	res, err := s.counter.CreateCounterOrder(ctx, sc, eid, ev.Type, co, ev.AgentStatus, ev.BatchID)
	if err != nil {
		return "", err
	}
	if res == order.FloorApplied {
		s.logger.Info().Ctx(ctx).Str("order_id", snap.ID).Str("status", string(co.Order.Status())).Msg("counter order stored")
		if s.onPlaced != nil {
			s.onPlaced(ctx, co.Order)
		}
	}
	return res, nil
}

func (s *Service) invalid(ctx context.Context, sc order.Scope, eid uuid.UUID, ev event, why error) (string, error) {
	s.logger.Warn().Ctx(ctx).Str("type", ev.Type).Str("order_id", ev.OrderID).Err(why).Msg("floor event refused")
	return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorInvalid)
}

func (s *Service) applyPayment(ctx context.Context, sc order.Scope, eid uuid.UUID, ev event) (string, error) {
	oid, err := uuid.Parse(ev.OrderID)
	if s.counter == nil || err != nil || len(ev.Payment) == 0 {
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
	return s.counter.AddCounterPayment(ctx, sc, eid, ev.Type, oid, cp)
}

func (s *Service) applyVoid(ctx context.Context, sc order.Scope, eid uuid.UUID, ev event) (string, error) {
	oid, err := uuid.Parse(ev.OrderID)
	reason := strings.TrimSpace(ev.Reason)
	var staffID *uuid.UUID
	if ev.StaffID != "" {
		id, perr := uuid.Parse(ev.StaffID)
		if perr != nil {
			err = perr
		}
		staffID = &id
	}
	if s.counter == nil || err != nil || reason == "" || utf8.RuneCountInString(reason) > staff.MaxReasonLen {
		return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorInvalid)
	}
	out, err := s.repo.ApplyFloorUpdate(ctx, sc, order.ID(oid), eid, ev.Type, func(o *order.Order) string {
		if o.Source() != order.SourceCounter {
			return order.FloorInvalid
		}
		return o.ApplyVoid(reason, staffID, ev.At)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return s.seen(ctx, sc, eid, &oid, ev.Type, order.FloorNotFound)
	}
	if err != nil {
		return "", err
	}
	if out == order.FloorApplied {
		s.logger.Info().Ctx(ctx).Str("order_id", oid.String()).Msg("counter order voided")
	}
	return out, nil
}

// applyChanged replaces a counter order's lines and money with the Pi's
// changed snapshot (a kitchen change after SEND, agent contract §2.10d):
// checked like order.placed's; payments, status and number stay.
func (s *Service) applyChanged(ctx context.Context, sc order.Scope, eid uuid.UUID, ev event) (string, error) {
	oid, err := uuid.Parse(ev.OrderID)
	if s.counter == nil || err != nil || len(ev.Snapshot) == 0 {
		return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorInvalid)
	}
	var snap order.CounterSnapshot
	if err := json.Unmarshal(ev.Snapshot, &snap); err != nil {
		return s.invalid(ctx, sc, eid, ev, err)
	}
	if snap.ID != ev.OrderID {
		return s.invalid(ctx, sc, eid, ev, errors.New("order_id differs from the snapshot's id"))
	}
	co, err := order.NewCounterOrder(sc, snap, ev.Snapshot)
	if err != nil {
		return s.invalid(ctx, sc, eid, ev, err)
	}
	res, err := s.counter.ChangeCounterOrder(ctx, sc, eid, ev.Type, oid, co.Order)
	if err != nil {
		return "", err
	}
	if res == order.FloorApplied {
		s.logger.Info().Ctx(ctx).Str("order_id", oid.String()).Int("lines", len(co.Order.Items())).Msg("counter order changed")
	}
	return res, nil
}

type journalEntry struct {
	ID       string          `json:"id"`
	At       time.Time       `json:"at"`
	StaffID  *string         `json:"staff_id"`
	DeviceID string          `json:"device_id"`
	Action   string          `json:"action"`
	OrderID  *string         `json:"order_id"`
	Detail   json.RawMessage `json:"detail"`
}

func optUUID(s *string) (*uuid.UUID, bool) {
	if s == nil || *s == "" {
		return nil, true
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		return nil, false
	}
	return &id, true
}

func (s *Service) applyJournal(ctx context.Context, sc order.Scope, eid uuid.UUID, ev event) (string, error) {
	if s.journal == nil || len(ev.Entries) == 0 {
		return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorInvalid)
	}
	var in []journalEntry
	if err := json.Unmarshal(ev.Entries, &in); err != nil || len(in) == 0 || len(in) > staff.MaxJournalBatch {
		return s.invalid(ctx, sc, eid, ev, errors.New("entries: 1–200 entries"))
	}
	entries := make([]staff.JournalEntry, 0, len(in))
	for _, e := range in {
		id, err := uuid.Parse(e.ID)
		staffID, ok1 := optUUID(e.StaffID)
		orderID, ok2 := optUUID(e.OrderID)
		action := strings.TrimSpace(e.Action)
		detail := []byte(e.Detail)
		if len(detail) == 0 || string(detail) == "null" {
			detail = []byte("{}")
		}
		var obj map[string]any
		if err != nil || !ok1 || !ok2 || e.At.IsZero() || action == "" || len(action) > staff.MaxJournalAction ||
			len(e.DeviceID) > staff.MaxDeviceIDLen || len(detail) > staff.MaxJournalDetail || json.Unmarshal(detail, &obj) != nil || obj == nil {
			return s.invalid(ctx, sc, eid, ev, errors.New("a journal entry needs id, at, action (≤ 40), uuids, device_id ≤ 64, detail an object ≤ 4 KB"))
		}
		entries = append(entries, staff.JournalEntry{ID: id, At: e.At.UTC(), StaffID: staffID, DeviceID: e.DeviceID, Action: action,
			OrderID: orderID, Detail: detail})
	}
	if _, err := s.journal.AddJournal(ctx, uuid.UUID(sc.TenantID), uuid.UUID(sc.LocationID), entries); err != nil {
		return "", err
	}
	return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorApplied)
}
