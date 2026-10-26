package geo

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/models"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// --- fake queries for location.radius

func (f *fakeDB) SetLocationDeliveryRadius(_ context.Context, a models.SetLocationDeliveryRadiusParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.inScope(a.TenantID, a.LocationID) {
		return 0, nil
	}
	f.store.DeliveryRadiusM = a.RadiusM
	return 1, nil
}

func (f *fakeDB) ListOpenLocatedOrderGeoForRadius(_ context.Context, a models.ListOpenLocatedOrderGeoForRadiusParams) ([]models.ListOpenLocatedOrderGeoForRadiusRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.ListOpenLocatedOrderGeoForRadiusRow
	for _, g := range f.orderGeo {
		if f.inScope(g.TenantID, g.LocationID) && g.State == StateLocated && g.Lat.Valid {
			out = append(out, models.ListOpenLocatedOrderGeoForRadiusRow{OrderID: g.OrderID, Lat: g.Lat, Lng: g.Lng, OutsideArea: g.OutsideArea})
		}
	}
	return out, nil
}

func (f *fakeDB) SetOrderGeoOutsideArea(_ context.Context, a models.SetOrderGeoOutsideAreaParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.orderGeo[fromPg(a.OrderID)]
	if !ok || g.State != StateLocated || g.OutsideArea == a.OutsideArea {
		return 0, nil
	}
	g.OutsideArea, g.PushedAt, g.NextTryAt, g.UpdatedAt = a.OutsideArea, pgtype.Timestamptz{}, ts(f.now()), f.stamp()
	return 1, nil
}

func (f *fakeDB) ListLiveExternalStopsForRadius(_ context.Context, a models.ListLiveExternalStopsForRadiusParams) ([]models.ListLiveExternalStopsForRadiusRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.ListLiveExternalStopsForRadiusRow
	for _, s := range f.stops {
		if f.inScope(s.TenantID, s.LocationID) && s.State == StateLocated && s.Lat.Valid && s.ExpiresAt.Time.After(f.now()) {
			out = append(out, models.ListLiveExternalStopsForRadiusRow{OrderID: s.OrderID, Lat: s.Lat, Lng: s.Lng, OutsideArea: s.OutsideArea})
		}
	}
	return out, nil
}

func (f *fakeDB) SetExternalStopOutsideArea(_ context.Context, a models.SetExternalStopOutsideAreaParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.stops[fromPg(a.OrderID)]
	if !ok || s.State != StateLocated || s.OutsideArea == a.OutsideArea {
		return 0, nil
	}
	s.OutsideArea, s.PushedAt, s.NextTryAt, s.UpdatedAt = a.OutsideArea, pgtype.Timestamptz{}, ts(f.now()), f.stamp()
	return 1, nil
}

// A new radius re-flags the open deliveries, re-pushes only the ones whose
// flag changed, pushes store_config and answers {radius_m}.
func TestLocationRadius(t *testing.T) {
	h := newHarness(t)
	var storePushes int
	h.svc.OnStoreChanged(func(context.Context, uuid.UUID, uuid.UUID) { storePushes++ })
	h.knows("1 Near St", cand("1 Near St", "Towson", "21204", 39.4100, -76.5895)) // ~1.3 km north
	h.knows("2 Far Rd", cand("2 Far Rd", "Towson", "21286", 39.4382, -76.5895))   // ~4.4 km north
	h.knows("3 Stop Ln", cand("3 Stop Ln", "Towson", "21286", 39.4382, -76.5800)) // ~4.5 km (a POS stop)
	near, far, stop := uuid.New(), uuid.New(), uuid.New()
	h.db.addOrder(near, order.Address{Line1: "1 Near St", City: "Towson", State: "MD", PostalCode: "21204"})
	h.db.addOrder(far, order.Address{Line1: "2 Far Rd", City: "Towson", State: "MD", PostalCode: "21286"})
	h.svc.q.InsertOrderGeo(context.Background(), insertParams(h, near))
	h.svc.q.InsertOrderGeo(context.Background(), insertParams(h, far))
	h.rpc(h.svc.HandleLocate, locateBody(stop, "3 Stop Ln", "21286"))
	h.drain()
	for _, p := range h.agent.take() {
		if p.OutsideArea {
			t.Fatalf("inside 5 mi at first: %+v", p)
		}
	}

	got := h.rpc(h.svc.HandleRadius, map[string]any{"radius_m": 3000})
	if got["radius_m"] != float64(3000) || len(got) != 1 || h.db.store.DeliveryRadiusM != 3000 || storePushes != 1 {
		t.Fatalf("reply %v radius %d store pushes %d", got, h.db.store.DeliveryRadiusM, storePushes)
	}
	h.drain()
	pushed := map[string]bool{}
	for _, p := range h.agent.take() {
		pushed[p.OrderID] = p.OutsideArea
	}
	if len(pushed) != 2 || !pushed[far.String()] || !pushed[stop.String()] {
		t.Fatalf("re-pushed %v (want far + stop, outside)", pushed)
	}
	if _, ok := pushed[near.String()]; ok {
		t.Fatal("an unchanged flag was pushed again")
	}

	// The same radius again: nothing flips, nothing is pushed.
	h.rpc(h.svc.HandleRadius, map[string]any{"radius_m": 3000})
	h.drain()
	if ps := h.agent.take(); len(ps) != 0 {
		t.Fatalf("pushes for an unchanged radius: %+v", ps)
	}

	for _, bad := range []string{`{}`, `{"radius_m":499}`, `{"radius_m":80468}`, `{"radius_m":1500.5}`, `{"radius_m":"x"}`, `[]`} {
		if _, err := h.svc.HandleRadius(context.Background(), h.scope, json.RawMessage(bad)); !errors.Is(err, rpc.ErrBadPayload) {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	if h.db.store.DeliveryRadiusM != 3000 {
		t.Fatal("a bad radius was saved")
	}
}
