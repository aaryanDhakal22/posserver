package floor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"quiccpos/main/internal/domain/order"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/observability"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// fakeFloor records "seen" events; any other repository call panics (nil
// embedded interface) — a POS stop must never touch an order.
type fakeFloor struct {
	order.FloorRepository
	seen map[uuid.UUID]string
}

func (f *fakeFloor) MarkFloorEventSeen(_ context.Context, _ order.Scope, id uuid.UUID, _ *uuid.UUID, _, result string) (bool, error) {
	if _, ok := f.seen[id]; ok {
		return true, nil
	}
	f.seen[id] = result
	return false, nil
}

type fakeRuns struct {
	runs map[uuid.UUID]order.DeliveryRun
}

func (f *fakeRuns) SaveDeliveryRun(_ context.Context, _ order.Scope, id uuid.UUID, r order.DeliveryRun) (string, error) {
	if _, ok := f.runs[id]; ok {
		return order.FloorDuplicate, nil
	}
	f.runs[id] = r
	return order.FloorApplied, nil
}

func (f *fakeRuns) ListDeliveryRuns(context.Context, order.Scope, int) ([]order.DeliveryRun, error) {
	return nil, nil
}

type noOrders struct{}

func (noOrders) GetByID(context.Context, order.Scope, order.ID) (*order.Order, error) {
	panic("delivery.run must not look orders up")
}

func TestDeliveryRunWithPOSStops(t *testing.T) {
	meters, err := observability.NewMeters()
	if err != nil {
		t.Fatal(err)
	}
	repo := &fakeFloor{seen: map[uuid.UUID]string{}}
	runs := &fakeRuns{runs: map[uuid.UUID]order.DeliveryRun{}}
	svc := NewService(noOrders{}, repo, zerolog.Nop(), meters)
	svc.OnDeliveryRun(runs)
	scope := sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}
	at := time.Date(2026, 10, 25, 18, 0, 0, 0, time.UTC).Format(time.RFC3339)
	online := uuid.NewString()
	posStop := map[string]any{"opaque_id": "pos:0123456789ab", "source": "pos", "placed_at": at, "sent_at": at,
		"out_at": at, "delivered_at": nil, "distance_m": 2400}

	mixed := map[string]any{"event_id": uuid.NewString(), "type": "delivery.run", "at": at, "driver_id": uuid.NewString(),
		"driver_name": "Sam", "sent_ids": []string{online}, "suggested_ids": []string{online},
		"stops": []any{map[string]any{"order_id": online, "eta": at}, posStop}}
	posOnly := map[string]any{"event_id": uuid.NewString(), "type": "delivery.run", "at": at, "driver_id": uuid.NewString(),
		"driver_name": "Ann", "stops": []any{posStop}}
	empty := map[string]any{"event_id": uuid.NewString(), "type": "delivery.run", "at": at, "sent_ids": []string{}, "stops": []any{}}
	badStop := map[string]any{"event_id": uuid.NewString(), "type": "delivery.run", "at": at, "sent_ids": []string{online},
		"stops": []any{map[string]any{"eta": at}}}

	raw, _ := json.Marshal(map[string]any{"events": []any{mixed, posOnly, empty, badStop}})
	out, err := svc.Handle(context.Background(), scope, raw)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range out.(map[string]any)["results"].([]Result) {
		got[r.EventID] = r.Result
	}
	want := map[string]string{
		mixed["event_id"].(string): order.FloorApplied, posOnly["event_id"].(string): order.FloorApplied,
		empty["event_id"].(string): order.FloorInvalid, badStop["event_id"].(string): order.FloorInvalid,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %s, want %s", k, got[k], v)
		}
	}

	// Stored as sent: the POS stop is opaque, never resolved to an order.
	r := runs.runs[uuid.MustParse(posOnly["event_id"].(string))]
	var stops []map[string]any
	if err := json.Unmarshal(r.Stops, &stops); err != nil || len(stops) != 1 {
		t.Fatalf("stops = %s", r.Stops)
	}
	if stops[0]["opaque_id"] != "pos:0123456789ab" || stops[0]["source"] != "pos" || stops[0]["distance_m"] != float64(2400) {
		t.Errorf("stop = %v", stops[0])
	}
	if len(r.SentIDs) != 0 || r.DriverName != "Ann" {
		t.Errorf("run = %+v", r)
	}
	if m := runs.runs[uuid.MustParse(mixed["event_id"].(string))]; len(m.SentIDs) != 1 || len(m.Stops) == 0 {
		t.Errorf("mixed run = %+v", m)
	}
}

// memFloor applies floor updates to in-memory orders (idempotent on the
// event id, like the repository) and keeps the agent statuses.
type memFloor struct {
	fakeFloor
	orders map[uuid.UUID]*order.Order
	types  map[uuid.UUID]string
	agent  map[uuid.UUID]string
}

func (m *memFloor) ApplyFloorUpdate(_ context.Context, _ order.Scope, id order.ID, eid uuid.UUID, typ string, apply func(o *order.Order) string) (string, error) {
	o, ok := m.orders[uuid.UUID(id)]
	if !ok {
		return "", pgx.ErrNoRows
	}
	if _, dup := m.seen[eid]; dup {
		return order.FloorDuplicate, nil
	}
	res := apply(o)
	m.seen[eid], m.types[eid] = res, typ
	return res, nil
}

func (m *memFloor) SetAgentStatus(_ context.Context, _ order.Scope, id uuid.UUID, st []byte, _ *int64, _ time.Time) error {
	m.agent[id] = string(st)
	return nil
}

func TestNotDeliveredEvents(t *testing.T) {
	meters, err := observability.NewMeters()
	if err != nil {
		t.Fatal(err)
	}
	newOrder := func() *order.Order {
		o, err := order.NewOrder(order.NewOrderInput{TenantID: order.TenantID(uuid.New()), LocationID: order.LocationID(uuid.New()),
			Source: order.SourceOnline, SourceEventID: "1", Currency: "USD", PlacedAt: time.Now().UTC(),
			Items: []order.LineItem{{ID: uuid.New(), Name: "Pizza", Quantity: 1, UnitPrice: 1000, LineTotal: 1000}},
			Money: order.MoneyBreakdown{Subtotal: 1000, Total: 1000}, RawPayload: []byte("{}")})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	retried, cancelled := newOrder(), newOrder()
	t0 := time.Date(2026, 10, 26, 18, 0, 0, 0, time.UTC)
	retried.ApplyFloorUpdate(order.FloorUpdate{Status: order.StatusOnDelivery, DriverName: "Sam", At: t0})
	cancelled.ApplyFloorUpdate(order.FloorUpdate{Status: order.StatusCompleted, DriverName: "Sam", At: t0})
	repo := &memFloor{fakeFloor: fakeFloor{seen: map[uuid.UUID]string{}}, types: map[uuid.UUID]string{}, agent: map[uuid.UUID]string{},
		orders: map[uuid.UUID]*order.Order{uuid.UUID(retried.ID()): retried, uuid.UUID(cancelled.ID()): cancelled}}
	svc := NewService(noOrders{}, repo, zerolog.Nop(), meters)
	scope := sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}
	at := t0.Add(5 * time.Minute).Format(time.RFC3339Nano)
	ev := func(orderID, action string) map[string]any {
		return map[string]any{"event_id": uuid.NewString(), "type": "order.not_delivered", "order_id": orderID, "action": action,
			"status": map[string]string{"retry": "ready", "cancel": "cancelled"}[action], "driver_id": uuid.NewString(),
			"driver_name": "Sam", "at": at, "batch_id": 7, "agent_status": map[string]any{"state": "ready", "came_back": true}}
	}
	retry := ev(uuid.UUID(retried.ID()).String(), "retry")
	cancel := ev(uuid.UUID(cancelled.ID()).String(), "cancel")
	unknown := ev(uuid.NewString(), "retry")
	bad := ev(uuid.UUID(retried.ID()).String(), "lost")

	run := func(evs ...any) map[string]string {
		raw, _ := json.Marshal(map[string]any{"events": evs})
		out, err := svc.Handle(context.Background(), scope, raw)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, r := range out.(map[string]any)["results"].([]Result) {
			got[r.EventID] = r.Result
		}
		return got
	}
	got := run(retry, cancel, unknown, bad)
	want := map[string]string{retry["event_id"].(string): order.FloorApplied, cancel["event_id"].(string): order.FloorApplied,
		unknown["event_id"].(string): order.FloorNotFound, bad["event_id"].(string): order.FloorInvalid}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %s, want %s", k, got[k], v)
		}
	}
	if retried.Status() != order.StatusReady || retried.DriverName() != "" {
		t.Errorf("retried: %s %q", retried.Status(), retried.DriverName())
	}
	if cancelled.Status() != order.StatusCancelled || cancelled.DriverName() != "" {
		t.Errorf("cancelled: %s %q", cancelled.Status(), cancelled.DriverName())
	}
	if typ := repo.types[uuid.MustParse(retry["event_id"].(string))]; typ != "order.not_delivered" {
		t.Errorf("seen as %q", typ)
	}
	if st := repo.agent[uuid.UUID(retried.ID())]; st == "" {
		t.Error("agent_status not kept")
	}
	// A replay is a duplicate; the retried order goes out again forward.
	if r := run(retry)[retry["event_id"].(string)]; r != order.FloorDuplicate {
		t.Errorf("replay: %s", r)
	}
	out := map[string]any{"event_id": uuid.NewString(), "type": "order.status", "order_id": uuid.UUID(retried.ID()).String(),
		"status": "on_delivery", "driver_name": "Ann", "at": t0.Add(10 * time.Minute).Format(time.RFC3339Nano)}
	if r := run(out)[out["event_id"].(string)]; r != order.FloorApplied || retried.Status() != order.StatusOnDelivery || retried.DriverName() != "Ann" {
		t.Errorf("out again: %s %s %q", r, retried.Status(), retried.DriverName())
	}
}
