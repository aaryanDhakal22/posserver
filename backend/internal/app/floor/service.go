// Package floor applies the restaurant agent's floor events (the inbound
// `floor.events` RPC report). The agent decides floor state — accept + kitchen
// number + prep estimate, ready, on delivery, delivered, and the driver
// roster — and main mirrors it: forward-only, idempotent on event_id, scoped
// to the reporting agent's (tenant, location).
package floor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"quiccpos/main/internal/app/dayclose"
	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/domain/staff"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/observability"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
)

// MsgFloorEvents is the RPC report type the agent sends.
const MsgFloorEvents = "floor.events"

// Result is the per-event verdict returned to the agent.
type Result struct {
	EventID string `json:"event_id"`
	Result  string `json:"result"`
}

type event struct {
	EventID          string     `json:"event_id"`
	Type             string     `json:"type"`
	OrderID          string     `json:"order_id"`
	Status           string     `json:"status"`
	KitchenNumber    *int       `json:"kitchen_number"`
	EstimatedReadyAt *time.Time `json:"estimated_ready_at"`
	DriverName       string     `json:"driver_name"`
	Auto             bool       `json:"auto"`
	At               time.Time  `json:"at"`
	// The agent's whole status object + batch (newer agents) — stored for a
	// replacement agent's restore.
	AgentStatus json.RawMessage `json:"agent_status"`
	BatchID     *int64          `json:"batch_id"`
	// driver.shift
	ShiftID  string     `json:"shift_id"`
	DriverID string     `json:"driver_id"`
	ClockIn  time.Time  `json:"clock_in"`
	ClockOut *time.Time `json:"clock_out"`
	// order.not_delivered (2.2.1): retry | cancel
	Action string `json:"action"`
	// order.tip (weborder stage 2): the whole tip added after the order, in cents
	TipCents *int64 `json:"tip_cents"`
	// order.status cancelled by hand (weborder stage 3): customer_asked | couldnt_deliver | other
	Reason string `json:"reason"`
	// delivery.run (2.0.9)
	PlanID       string          `json:"plan_id"`
	SuggestedIDs []string        `json:"suggested_ids"`
	SentIDs      []string        `json:"sent_ids"`
	Stops        json.RawMessage `json:"stops"`
	// Counter orders (TERMINAL-PLAN 0d): order.placed's snapshot,
	// order.payment's payment, order.void's staff, journal.entries' entries.
	Snapshot json.RawMessage `json:"snapshot"`
	Payment  json.RawMessage `json:"payment"`
	StaffID  string          `json:"staff_id"`
	Entries  json.RawMessage `json:"entries"`
	// customer.upsert (terminal 1d): the customer + which fields it sets.
	Customer json.RawMessage `json:"customer"`
	Set      []string        `json:"set"`
	// Drawers + the Z report (terminal Stage 2): drawer.opened / .closed's
	// drawer, drawer.movement's movement, z.report's report.
	Drawer   json.RawMessage `json:"drawer"`
	Movement json.RawMessage `json:"movement"`
	Report   json.RawMessage `json:"report"`
	Drivers  []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Position int    `json:"position"`
		Status   string `json:"status"`
	} `json:"drivers"`
}

// OrderLoader is the slice of the order repository the service reads.
type OrderLoader interface {
	GetByID(ctx context.Context, scope order.Scope, id order.ID) (*order.Order, error)
}

// DeliveryRunStore keeps delivery.run events (idempotent on the event id):
// returns applied or duplicate.
type DeliveryRunStore interface {
	SaveDeliveryRun(ctx context.Context, sc order.Scope, eventID uuid.UUID, r order.DeliveryRun) (string, error)
	ListDeliveryRuns(ctx context.Context, sc order.Scope, limit int) ([]order.DeliveryRun, error)
}

// CounterOrders stores the Pi's counter orders and their payments
// (repositories.OrderRepository).
type CounterOrders interface {
	CreateCounterOrder(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, co *order.CounterOrder, agentStatus []byte, batchID *int64) (string, error)
	AddCounterPayment(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, orderID uuid.UUID, p order.CheckedPayment) (string, error)
	ChangeCounterOrder(ctx context.Context, sc order.Scope, eventID uuid.UUID, typ string, orderID uuid.UUID, o *order.Order) (string, error)
}

// Customers applies the counter's customer.upsert (the customers service):
// an error wrapping customers.ErrBadCounterCustomer is answered invalid.
type Customers interface {
	ApplyCounterEvent(ctx context.Context, tenantID, locationID, eventID uuid.UUID, typ string, raw json.RawMessage, set []string) (string, error)
}

// Journal keeps the terminal's staff journal (the staff service).
type Journal interface {
	AddJournal(ctx context.Context, tenantID, locationID uuid.UUID, entries []staff.JournalEntry) (int, error)
}

// DayClose stores the Pi's drawer sessions and Z reports (the dayclose
// service): a *dayclose.Refused error is answered invalid.
type DayClose interface {
	Apply(ctx context.Context, sc order.Scope, eventID uuid.UUID, ev dayclose.Event) (string, error)
}

type Service struct {
	dayClose  DayClose
	counter   CounterOrders
	journal   Journal
	customers Customers
	onPlaced  func(ctx context.Context, o *order.Order)
	orders    OrderLoader
	repo      order.FloorRepository
	runs      DeliveryRunStore
	holds     CardHolds
	onCancel  func(ctx context.Context, orderID uuid.UUID)
	logger    zerolog.Logger
	meters    observability.Meters
}

// CardHolds captures / voids online card holds as the floor moves (the
// cardhold service, weborder stage 2).
type CardHolds interface {
	DriverClockedOut(ctx context.Context, locationID uuid.UUID, driverID string, clockIn, clockOut time.Time)
	BatchClosed(ctx context.Context, locationID uuid.UUID, batchID int64)
	// SetTip sets the tip added after the order (false = no open card hold).
	SetTip(ctx context.Context, locationID, orderID uuid.UUID, cents int64) (bool, error)
	OrderCancelled(ctx context.Context, locationID, orderID uuid.UUID)
}

// OnCardHolds plugs in the card holds (nil = order.tip / batch.closed are
// answered invalid).
func (s *Service) OnCardHolds(h CardHolds) { s.holds = h }

// OnCancelled is told after a cancelled order's card hold was dealt with
// (the customer's final receipt).
func (s *Service) OnCancelled(f func(ctx context.Context, orderID uuid.UUID)) { s.onCancel = f }

// OnCounter plugs in counter orders + the staff journal (nil = the 0d event
// types are answered invalid, as by an older main).
func (s *Service) OnCounter(c CounterOrders, j Journal) { s.counter, s.journal = c, j }

// OnCustomers plugs in the counter's customers (nil = customer.upsert is
// answered invalid, as by an older main).
func (s *Service) OnCustomers(c Customers) { s.customers = c }

// OnDayClose plugs in drawers + Z reports (nil = drawer.* / z.report are
// answered invalid, as by an older main).
func (s *Service) OnDayClose(d DayClose) { s.dayClose = d }

// OnCounterPlaced is told after a counter order was stored (the geo worker
// locates a counter delivery).
func (s *Service) OnCounterPlaced(f func(ctx context.Context, o *order.Order)) { s.onPlaced = f }

// MaxTipCents bounds an added tip ($500).
const MaxTipCents = 50000

// OnDeliveryRun sets where delivery.run events are stored (nil = answered
// invalid, as before 2.0.9).
func (s *Service) OnDeliveryRun(st DeliveryRunStore) { s.runs = st }

func NewService(orders OrderLoader, repo order.FloorRepository, logger zerolog.Logger, meters observability.Meters) *Service {
	return &Service{orders: orders, repo: repo, logger: logger.With().Str("module", "floor").Logger(), meters: meters}
}

// Handle is the rpc.InboundHandler for floor.events. An infrastructure error
// fails the whole POST (the agent retries; already-applied events come back
// as duplicates); per-event problems are results, not errors.
func (s *Service) Handle(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var body struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, fmt.Errorf("%w: floor.events: %v", rpc.ErrBadPayload, err)
	}
	sc := order.Scope{TenantID: order.TenantID(scope.TenantID), LocationID: order.LocationID(scope.LocationID)}
	results := make([]Result, 0, len(body.Events))
	for _, raw := range body.Events {
		var ev event
		if err := json.Unmarshal(raw, &ev); err != nil {
			// Can't decode it, but answer for its event_id so it doesn't block
			// the agent's sync forever.
			var id struct {
				EventID string `json:"event_id"`
			}
			if json.Unmarshal(raw, &id) == nil && id.EventID != "" {
				results = append(results, Result{EventID: id.EventID, Result: order.FloorInvalid})
			}
			continue
		}
		res, err := s.apply(ctx, sc, ev)
		if err != nil {
			return nil, err
		}
		s.meters.FloorEventsReceived.Add(ctx, 1, observability.WithStore(ctx, scope.TenantID,
			attribute.String("type", ev.Type), attribute.String("result", res)))
		if res == order.FloorApplied {
			// Only applied events: a duplicate / stale replay would re-measure
			// an old event. `type` is bounded — unknown types are never applied.
			observability.RecordLatency(ctx, s.meters.FloorSyncLatency, ev.At, time.Now(),
				observability.WithStore(ctx, scope.TenantID, attribute.String("type", ev.Type)))
		}
		results = append(results, Result{EventID: ev.EventID, Result: res})
	}
	return map[string]any{"results": results}, nil
}

func (s *Service) apply(ctx context.Context, sc order.Scope, ev event) (string, error) {
	eid, err := uuid.Parse(ev.EventID)
	if err != nil {
		return order.FloorInvalid, nil
	}
	switch ev.Type {
	case "order.status":
		return s.applyStatus(ctx, sc, eid, ev)
	case "order.not_delivered":
		return s.applyNotDelivered(ctx, sc, eid, ev)
	case "drivers.roster":
		drivers := make([]order.Driver, 0, len(ev.Drivers))
		for _, d := range ev.Drivers {
			id, err := uuid.Parse(d.ID)
			if err != nil {
				return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorInvalid)
			}
			drivers = append(drivers, order.Driver{ID: id, Name: d.Name, Position: d.Position, Status: d.Status})
		}
		return s.repo.ReplaceDrivers(ctx, sc, eid, drivers)
	case "driver.shift":
		sid, err1 := uuid.Parse(ev.ShiftID)
		did, err2 := uuid.Parse(ev.DriverID)
		if err1 != nil || err2 != nil || ev.ClockIn.IsZero() {
			return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorInvalid)
		}
		res, err := s.repo.SaveDriverShift(ctx, sc, eid, order.DriverShift{ID: sid, DriverID: did, DriverName: ev.DriverName,
			ClockIn: ev.ClockIn, ClockOut: ev.ClockOut, AutoOut: ev.Auto})
		if err == nil && res == order.FloorApplied && ev.ClockOut != nil && s.holds != nil {
			// The driver's card deliveries of this shift are captured now.
			s.holds.DriverClockedOut(ctx, uuid.UUID(sc.LocationID), did.String(), ev.ClockIn, *ev.ClockOut)
		}
		return res, err
	case "order.tip":
		oid, err := uuid.Parse(ev.OrderID)
		if s.holds == nil || err != nil || ev.TipCents == nil || *ev.TipCents < 0 || *ev.TipCents > MaxTipCents {
			return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorInvalid)
		}
		// Absolute (the whole added tip), so a replay is harmless: set it, then mark the event seen.
		ok, err := s.holds.SetTip(ctx, uuid.UUID(sc.LocationID), oid, *ev.TipCents)
		if err != nil {
			return "", err
		}
		if !ok {
			return s.seen(ctx, sc, eid, &oid, ev.Type, order.FloorStale) // captured already / not a card order
		}
		return s.seen(ctx, sc, eid, &oid, ev.Type, order.FloorApplied)
	case "batch.closed":
		if s.holds == nil || ev.BatchID == nil {
			return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorInvalid)
		}
		res, err := s.seen(ctx, sc, eid, nil, ev.Type, order.FloorApplied)
		if err == nil && res == order.FloorApplied {
			s.holds.BatchClosed(ctx, uuid.UUID(sc.LocationID), *ev.BatchID)
		}
		return res, err
	case "delivery.run":
		// Stops are stored as sent — no order lookup. Every stop needs an
		// order_id (SoftTouch's opaque POS stops are refused since H3).
		okStops, nStops := order.ValidRunStops(ev.Stops)
		if s.runs == nil || ev.At.IsZero() || !okStops || (len(ev.SentIDs) == 0 && nStops == 0) {
			return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorInvalid)
		}
		stops := ev.Stops
		if nStops == 0 {
			stops = json.RawMessage("[]")
		}
		return s.runs.SaveDeliveryRun(ctx, sc, eid, order.DeliveryRun{
			BatchID: ev.BatchID, DriverID: ev.DriverID, DriverName: ev.DriverName, PlanID: ev.PlanID,
			SuggestedIDs: ev.SuggestedIDs, SentIDs: ev.SentIDs, Stops: stops, At: ev.At,
		})
	case "order.placed":
		return s.applyPlaced(ctx, sc, eid, ev)
	case "order.payment":
		return s.applyPayment(ctx, sc, eid, ev)
	case "order.void":
		return s.applyVoid(ctx, sc, eid, ev)
	case "order.changed":
		return s.applyChanged(ctx, sc, eid, ev)
	case "journal.entries":
		return s.applyJournal(ctx, sc, eid, ev)
	case "customer.upsert":
		return s.applyCustomer(ctx, sc, eid, ev)
	case dayclose.EventDrawerOpened, dayclose.EventDrawerMovement, dayclose.EventDrawerClosed, dayclose.EventZReport:
		return s.applyDayClose(ctx, sc, eid, ev)
	default:
		return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorInvalid)
	}
}

func (s *Service) applyStatus(ctx context.Context, sc order.Scope, eid uuid.UUID, ev event) (string, error) {
	status, ok := order.FloorStatus(ev.Status)
	oid, err := uuid.Parse(ev.OrderID)
	if !ok || err != nil {
		return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorInvalid)
	}
	var newStatus order.Status
	out, err := s.repo.ApplyFloorUpdate(ctx, sc, order.ID(oid), eid, ev.Type, func(o *order.Order) string {
		res := o.ApplyFloorUpdate(order.FloorUpdate{
			Status: status, KitchenNumber: ev.KitchenNumber, EstimatedReadyAt: ev.EstimatedReadyAt,
			DriverName: ev.DriverName, At: ev.At, Auto: ev.Auto, Reason: ev.Reason,
		})
		newStatus = o.Status()
		return res
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Not this agent's order (or deleted from main) — permanent.
		return s.seen(ctx, sc, eid, &oid, ev.Type, order.FloorNotFound)
	}
	if err != nil {
		return "", err
	}
	if out == order.FloorApplied {
		s.logger.Info().Ctx(ctx).Str("order_id", oid.String()).Str("status", string(newStatus)).Msg("floor update applied")
		s.cancelled(ctx, sc, oid, newStatus)
	}
	return out, s.keepAgentStatus(ctx, sc, oid, ev)
}

// keepAgentStatus stores the agent's own view of the order (even for a
// skipped / duplicate event — the store is guarded by `at`) so a replacement
// Pi can restore it.
func (s *Service) keepAgentStatus(ctx context.Context, sc order.Scope, oid uuid.UUID, ev event) error {
	if len(ev.AgentStatus) > 0 && string(ev.AgentStatus) != "null" && !ev.At.IsZero() {
		return s.repo.SetAgentStatus(ctx, sc, oid, ev.AgentStatus, ev.BatchID, ev.At)
	}
	return nil
}

// applyNotDelivered: a delivery came back with its driver (2.2.1) — retry →
// ready with no driver, cancel → cancelled. The only floor event that may move
// an order backwards (order.ApplyNotDelivered).
func (s *Service) applyNotDelivered(ctx context.Context, sc order.Scope, eid uuid.UUID, ev event) (string, error) {
	oid, err := uuid.Parse(ev.OrderID)
	if err != nil || (ev.Action != order.NotDeliveredRetry && ev.Action != order.NotDeliveredCancel) {
		return s.seen(ctx, sc, eid, nil, ev.Type, order.FloorInvalid)
	}
	var newStatus order.Status
	out, err := s.repo.ApplyFloorUpdate(ctx, sc, order.ID(oid), eid, ev.Type, func(o *order.Order) string {
		res := o.ApplyNotDelivered(order.NotDeliveredUpdate{Action: ev.Action, At: ev.At, Auto: ev.Auto})
		newStatus = o.Status()
		return res
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return s.seen(ctx, sc, eid, &oid, ev.Type, order.FloorNotFound)
	}
	if err != nil {
		return "", err
	}
	if out == order.FloorApplied {
		s.logger.Info().Ctx(ctx).Str("order_id", oid.String()).Str("action", ev.Action).Str("status", string(newStatus)).
			Msg("not delivered applied")
		s.cancelled(ctx, sc, oid, newStatus)
	}
	return out, s.keepAgentStatus(ctx, sc, oid, ev)
}

func (s *Service) seen(ctx context.Context, sc order.Scope, eid uuid.UUID, oid *uuid.UUID, typ, result string) (string, error) {
	dup, err := s.repo.MarkFloorEventSeen(ctx, sc, eid, oid, typ, result)
	if err != nil {
		return "", err
	}
	if dup {
		return order.FloorDuplicate, nil
	}
	return result, nil
}

// Drivers returns the location's mirrored roster.
func (s *Service) Drivers(ctx context.Context, sc order.Scope) ([]order.Driver, error) {
	return s.repo.ListDrivers(ctx, sc)
}

// Shifts returns the location's driver shifts that started since `since`.
func (s *Service) Shifts(ctx context.Context, sc order.Scope, since time.Time) ([]order.DriverShift, error) {
	return s.repo.ListDriverShifts(ctx, sc, since)
}

// LastSyncAt is when the location's agent last synced a floor event.
func (s *Service) LastSyncAt(ctx context.Context, sc order.Scope) (*time.Time, error) {
	return s.repo.LastFloorEventAt(ctx, sc)
}

// DeliveryRuns returns the location's newest driver runs (delivery.run).
func (s *Service) DeliveryRuns(ctx context.Context, sc order.Scope, limit int) ([]order.DeliveryRun, error) {
	if s.runs == nil {
		return []order.DeliveryRun{}, nil
	}
	return s.runs.ListDeliveryRuns(ctx, sc, limit)
}

// cancelled releases a cancelled order's card hold (in the background: it
// talks to the processor and must not hold up the agent's sync).
func (s *Service) cancelled(ctx context.Context, sc order.Scope, oid uuid.UUID, st order.Status) {
	if st != order.StatusCancelled || (s.holds == nil && s.onCancel == nil) {
		return
	}
	ctx = context.WithoutCancel(ctx)
	go func() {
		if s.holds != nil {
			s.holds.OrderCancelled(ctx, uuid.UUID(sc.LocationID), oid)
		}
		if s.onCancel != nil {
			s.onCancel(ctx, oid)
		}
	}()
}
