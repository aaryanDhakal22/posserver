package floor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"quiccpos/main/internal/app/customers"
	"quiccpos/main/internal/app/dayclose"
	"quiccpos/main/internal/domain/notify"
	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/domain/staff"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Counter orders from the Pi (TERMINAL-PLAN.md 0d, contract/main.md §2.5zb):
// order.placed, order.payment, order.void, order.changed and journal.entries.
// order.payment takes any order of the store (a counter order, or an online /
// website pickup paid at the counter); the others only counter orders.

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
	for i := range co.Payments {
		s.who(ctx, sc, &co.Payments[i])
	}
	res, flags, err := s.counter.CreateCounterOrder(ctx, sc, eid, ev.Type, co, ev.AgentStatus, ev.BatchID)
	if err != nil {
		return "", err
	}
	if res == order.FloorApplied {
		s.logger.Info().Ctx(ctx).Str("order_id", snap.ID).Str("status", string(co.Order.Status())).
			Int64("total_cents", int64(co.Order.Money().Total)).Msg("counter order stored")
		for _, p := range co.Payments {
			s.meters.RecordCounterPayment(ctx, uuid.UUID(sc.LocationID), p.Method, int64(p.Amount), int64(p.Tip))
		}
		s.flagged(ctx, sc, eid, ev.Type, snap.ID, flags)
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
	if cp.BatchID == nil {
		cp.BatchID = ev.BatchID
	}
	cp.EarlierDay = cp.EarlierDay || ev.EarlierDay
	s.who(ctx, sc, &cp)
	res, flags, err := s.counter.AddCounterPayment(ctx, sc, eid, ev.Type, oid, cp)
	if err != nil {
		return "", err
	}
	switch res {
	case order.FloorApplied:
		e := s.logger.Info()
		if cp.Amount < 0 {
			e = s.logger.Warn() // L-3: refunds at Warn, with who gave them
		}
		e = e.Ctx(ctx).Str("order_id", oid.String()).Str("payment_id", cp.ID.String()).Str("method", cp.Method).
			Int64("amount_cents", int64(cp.Amount)).Int64("tip_cents", int64(cp.Tip)).Str("approved_by", cp.ApprovedBy)
		if cp.StaffID != nil {
			e = e.Str("staff_id", cp.StaffID.String())
		}
		e.Msg("terminal payment recorded")
		s.meters.RecordCounterPayment(ctx, uuid.UUID(sc.LocationID), cp.Method, int64(cp.Amount), int64(cp.Tip))
		s.flagged(ctx, sc, eid, ev.Type, oid.String(), flags)
	case order.FloorConflict:
		s.flagged(ctx, sc, eid, ev.Type, oid.String(), flags)
	}
	return res, nil
}

func (s *Service) applyVoid(ctx context.Context, sc order.Scope, eid uuid.UUID, ev event) (string, error) {
	oid, err := uuid.Parse(ev.OrderID)
	reason := strings.TrimSpace(ev.Reason)
	// A void is the Pi's record (its refunds came first as order.payment):
	// stored whatever its reason looks like (M-2), never refused for it.
	if reason == "" {
		reason = "no reason given"
	}
	if utf8.RuneCountInString(reason) > staff.MaxReasonLen {
		reason = string([]rune(reason)[:staff.MaxReasonLen])
	}
	// kind waste (the owner, 2026-11-12): made already, nothing prints; the
	// reason is one of the store's waste_reasons and the Z lists it.
	if ev.Kind == "waste" {
		w := strings.TrimSpace(ev.WasteReason)
		switch {
		case w == "":
			reason = "waste: " + reason
		case reason == "no reason given" || reason == w:
			reason = "waste: " + w
		default:
			reason = "waste: " + w + " (" + reason + ")"
		}
		if utf8.RuneCountInString(reason) > staff.MaxReasonLen+20 {
			reason = string([]rune(reason)[:staff.MaxReasonLen+20])
		}
	}
	var staffID *uuid.UUID
	if ev.StaffID != "" {
		id, perr := uuid.Parse(ev.StaffID)
		if perr != nil {
			err = perr
		}
		staffID = &id
	}
	if s.counter == nil || err != nil {
		return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorInvalid)
	}
	out, err := s.repo.ApplyFloorUpdate(ctx, sc, order.ID(oid), eid, ev.Type, func(o *order.Order) string {
		// A counter order, or an online vendor order the Pi voided at a
		// terminal (agent 2.5.3: a cash one, nothing paid online; the vendor
		// isn't told). A website / channel order is cancelled another way.
		if o.Source() != order.SourceCounter && o.Source() != order.SourceOnline {
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
		e := s.logger.Info().Ctx(ctx).Str("order_id", oid.String()).Str("reason", reason).Str("kind", ev.Kind)
		if staffID != nil {
			e = e.Str("staff_id", staffID.String())
		}
		e.Msg("counter order voided")
		// A void of a sent order is a manager's (money-rules.json voids):
		// one nobody approved, by someone with limits, is stored and flagged.
		if flags := s.voidFlags(ctx, sc, staffID, ev.ApprovedBy); len(flags) > 0 {
			if err := s.counter.FlagMoneyEvent(ctx, sc, eid, ev.Type, "order:"+oid.String(), flags); err != nil {
				s.logger.Warn().Ctx(ctx).Err(err).Str("order_id", oid.String()).Msg("void flag not stored")
			}
			s.flagged(ctx, sc, eid, ev.Type, oid.String(), flags)
		}
	}
	return out, nil
}

// voidFlags: order.VoidFlags for the person main's staff table says voided
// it (no staff service / a failed read / nobody named = no flag).
func (s *Service) voidFlags(ctx context.Context, sc order.Scope, staffID *uuid.UUID, approvedBy string) []order.MoneyFlag {
	if s.staff == nil || staffID == nil {
		return nil
	}
	people, err := s.staff.Refunders(ctx, uuid.UUID(sc.LocationID))
	if err != nil {
		s.logger.Warn().Ctx(ctx).Err(err).Msg("staff for the money rules (no permission flags on this event)")
		return nil
	}
	return order.VoidFlags(order.Who(people, staffID), approvedBy)
}

// applyChanged replaces a counter order's lines and money with the Pi's
// changed snapshot (a kitchen change after SEND, agent contract §2.10d):
// checked like order.placed's; payments, status and number stay. An online /
// website order the store edited (agent 2.10aa: online orders = called-in
// orders) changes the same way, keeps its source and its channel payment
// (a web order's card hold is never touched: the manager refunds by hand),
// and notes what it owes the customer back (refund_owed_cents, refund_due).
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
	co, src, err := order.NewChangedOrder(sc, snap, ev.Snapshot)
	if err != nil {
		return s.invalid(ctx, sc, eid, ev, err)
	}
	extra := order.ChangeExtra{RefundOwedCents: ev.RefundOwedCents}
	if src != order.SourceCounter {
		extra.Source = src
	}
	if len(ev.RefundDue) > 0 && string(ev.RefundDue) != "null" {
		if err := json.Unmarshal(ev.RefundDue, &extra.RefundDue); err != nil {
			return s.invalid(ctx, sc, eid, ev, fmt.Errorf("refund_due: %w", err))
		}
	}
	// The order as it was, for the geo worker (a delivery's new address is
	// located again). Only for a delivery: nothing to locate otherwise.
	var before *order.Order
	if s.onMoved != nil && s.orders != nil && co.Order.Fulfillment().Type == order.FulfillmentDelivery {
		if b, gerr := s.orders.GetByID(ctx, sc, order.ID(oid)); gerr == nil {
			before = b
		}
	}
	res, flags, err := s.counter.ChangeCounterOrder(ctx, sc, eid, ev.Type, oid, co.Order, ev.At, co.Flags, extra)
	if err != nil {
		return "", err
	}
	if res == order.FloorApplied {
		e := s.logger.Info().Ctx(ctx).Str("order_id", oid.String()).Str("source", string(src)).Int("lines", len(co.Order.Items())).
			Int64("total_cents", int64(co.Order.Money().Total))
		if extra.RefundOwedCents != nil {
			e = e.Int64("refund_owed_cents", *extra.RefundOwedCents)
		}
		e.Msg("order changed by the store")
		s.flagged(ctx, sc, eid, ev.Type, oid.String(), flags)
		if s.onMoved != nil && co.Order.Fulfillment().Type == order.FulfillmentDelivery {
			s.onMoved(ctx, before, co.Order)
		}
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
	res, err := s.seen(ctx, sc, eid, nil, ev.Type, order.FloorApplied)
	if err == nil && res == order.FloorApplied {
		// CM-13: once per event (a replayed event is a duplicate; entries
		// themselves are idempotent by id).
		for i, e := range entries {
			s.meters.RecordJournal(ctx, uuid.UUID(sc.LocationID), e.Action, roleOf(in[i].Detail))
		}
	}
	return res, err
}

// applyCustomer is the counter's customer.upsert (terminal 1d, contract
// §2.5zc): a customer made or changed at the counter.
func (s *Service) applyCustomer(ctx context.Context, sc order.Scope, eid uuid.UUID, ev event) (string, error) {
	if s.customers == nil {
		return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorInvalid)
	}
	res, err := s.customers.ApplyCounterEvent(ctx, uuid.UUID(sc.TenantID), uuid.UUID(sc.LocationID), eid, ev.Type, ev.Customer, ev.Set)
	if errors.Is(err, customers.ErrBadCounterCustomer) {
		return s.invalid(ctx, sc, eid, ev, err)
	}
	return res, err
}

// applyDayClose stores the Pi's drawer sessions, pay-ins / pay-outs and Z
// reports (terminal Stage 2, contract §2.5zb).
func (s *Service) applyDayClose(ctx context.Context, sc order.Scope, eid uuid.UUID, ev event) (string, error) {
	if s.dayClose == nil {
		return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorInvalid)
	}
	res, flags, err := s.dayClose.Apply(ctx, sc, eid, dayclose.Event{Type: ev.Type, Drawer: ev.Drawer, Movement: ev.Movement, Report: ev.Report})
	var ref *dayclose.Refused
	if errors.As(err, &ref) {
		return s.invalid(ctx, sc, eid, ev, ref.Err)
	}
	if err == nil && res == order.FloorApplied {
		s.flagged(ctx, sc, eid, ev.Type, "", flags)
	}
	return res, err
}

// roleOf reads a journal entry's detail.role (CM-13), "" when none.
func roleOf(detail json.RawMessage) string {
	var d struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(detail, &d)
	return d.Role
}

// who puts the payment's person and the store's people on it (main's staff
// table: role + effective refund limit), for the rule flags the repository
// works out with the order's earlier payments (order.PaymentFlags, the Pi's
// rules: contract/money-rules.json). No staff service / a failed read =
// nobody known: no permission flag.
func (s *Service) who(ctx context.Context, sc order.Scope, p *order.CheckedPayment) {
	if s.staff == nil {
		return
	}
	people, err := s.staff.Refunders(ctx, uuid.UUID(sc.LocationID))
	if err != nil {
		s.logger.Warn().Ctx(ctx).Err(err).Msg("staff for the money rules (no permission flags on this event)")
		return
	}
	p.People = people
	p.By = order.Who(people, p.StaffID)
}

// flagged logs, counts and (at most once an hour per store) pages money main
// stored but flagged.
func (s *Service) flagged(ctx context.Context, sc order.Scope, eid uuid.UUID, typ, subject string, flags []order.MoneyFlag) {
	if len(flags) == 0 {
		return
	}
	codes := make([]string, 0, len(flags))
	for _, f := range flags {
		codes = append(codes, f.Code)
		s.meters.RecordMoneyFlag(ctx, uuid.UUID(sc.LocationID), typ, f.Code)
	}
	s.logger.Warn().Ctx(ctx).Str("event_id", eid.String()).Str("type", typ).Str("order_id", subject).Strs("flags", codes).
		Msg("money event flagged (stored)")
	key := uuid.UUID(sc.LocationID).String()
	if s.notifier == nil || s.flagPages == nil || !s.flagPages.Allow(key) {
		return
	}
	s.flagPages.Add(key)
	_ = s.notifier.Notify(context.WithoutCancel(ctx), notify.Notice{LocationID: uuid.UUID(sc.LocationID), Kind: notify.KindPaymentProblem,
		Title: "Money to check", Message: fmt.Sprintf("main stored a %s it doesn't agree with (%s). See Store › Reports › Money notes.", typ, strings.Join(codes, ", "))})
}
