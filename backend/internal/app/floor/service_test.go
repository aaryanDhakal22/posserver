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
