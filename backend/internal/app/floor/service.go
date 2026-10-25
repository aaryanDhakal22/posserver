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

	"quiccpos/main/internal/domain/order"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/observability"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
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
	// delivery.run (2.0.9)
	PlanID       string          `json:"plan_id"`
	SuggestedIDs []string        `json:"suggested_ids"`
	SentIDs      []string        `json:"sent_ids"`
	Stops        json.RawMessage `json:"stops"`
	Drivers      []struct {
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

type Service struct {
	orders OrderLoader
	repo   order.FloorRepository
	runs   DeliveryRunStore
	logger zerolog.Logger
	meters observability.Meters
}

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
		s.meters.FloorEventsReceived.Add(ctx, 1, metric.WithAttributes(
			attribute.String("type", ev.Type), attribute.String("result", res)))
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
		return s.repo.SaveDriverShift(ctx, sc, eid, order.DriverShift{ID: sid, DriverID: did, DriverName: ev.DriverName,
			ClockIn: ev.ClockIn, ClockOut: ev.ClockOut, AutoOut: ev.Auto})
	case "delivery.run":
		// POS stops (2.1.0) are anonymous ({opaque_id, source:"pos", …}) and
		// stored as sent — no order lookup. A POS-only run has no sent_ids.
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
	out, err := s.repo.ApplyFloorUpdate(ctx, sc, order.ID(oid), eid, func(o *order.Order) string {
		res := o.ApplyFloorUpdate(order.FloorUpdate{
			Status: status, KitchenNumber: ev.KitchenNumber, EstimatedReadyAt: ev.EstimatedReadyAt,
			DriverName: ev.DriverName, At: ev.At, Auto: ev.Auto,
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
	}
	// Keep the agent's own view of the order (even for a skipped / duplicate
	// event — the store is guarded by `at`) so a replacement Pi can restore it.
	if len(ev.AgentStatus) > 0 && string(ev.AgentStatus) != "null" && !ev.At.IsZero() {
		if err := s.repo.SetAgentStatus(ctx, sc, oid, ev.AgentStatus, ev.BatchID, ev.At); err != nil {
			return "", err
		}
	}
	return out, nil
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
