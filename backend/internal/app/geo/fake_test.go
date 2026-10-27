package geo

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/infra/database/models"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/rs/zerolog"
)

// fakeDB emulates the geo queries in memory (one store). Embedding the
// Queries interface means a query a test did not expect panics.
type fakeDB struct {
	Queries
	mu       sync.Mutex
	now      func() time.Time
	seq      int64
	cache    map[string]models.GeoCache
	orders   map[uuid.UUID]models.GetOrderForGeoRow
	orderGeo map[uuid.UUID]*models.OrderGeo
	stops    map[uuid.UUID]*models.GeoExternalStop
	work     map[uuid.UUID]*models.GeoExternalWork
	store    models.GetLocationGeoRow
	scope    sseBroker.Scope
	wakes    int
	// afterStopSave runs right after a successful SaveExternalStopResult
	// (outside the lock): a test plays a concurrent RPC there.
	afterStopSave func()
}

func newFakeDB(now func() time.Time, scope sseBroker.Scope, store Point) *fakeDB {
	return &fakeDB{
		now: now, scope: scope,
		cache:    map[string]models.GeoCache{},
		orders:   map[uuid.UUID]models.GetOrderForGeoRow{},
		orderGeo: map[uuid.UUID]*models.OrderGeo{},
		stops:    map[uuid.UUID]*models.GeoExternalStop{},
		work:     map[uuid.UUID]*models.GeoExternalWork{},
		store: models.GetLocationGeoRow{Lat: pgtype.Float8{Float64: store.Lat, Valid: true},
			Lng: pgtype.Float8{Float64: store.Lng, Valid: true}, DeliveryRadiusM: 8047},
	}
}

func fromPg(u pgtype.UUID) uuid.UUID { return uuid.UUID(u.Bytes) }

// stamp is a strictly increasing "now()" so updated_at guards behave.
func (f *fakeDB) stamp() pgtype.Timestamptz {
	f.seq++
	return ts(f.now().Add(time.Duration(f.seq)))
}

func (f *fakeDB) inScope(t, l pgtype.UUID) bool {
	return fromPg(t) == f.scope.TenantID && fromPg(l) == f.scope.LocationID
}

// addOrder stores an online delivery order (an orders row) at the store.
func (f *fakeDB) addOrder(id uuid.UUID, a order.Address) {
	raw, _ := json.Marshal(a)
	f.orders[id] = models.GetOrderForGeoRow{ID: pgUUID(id), TenantID: pgUUID(f.scope.TenantID), LocationID: pgUUID(f.scope.LocationID),
		FulfillmentType: "delivery", FulfillmentAddress: raw, Status: "received",
		StoreLat: f.store.Lat, StoreLng: f.store.Lng, DeliveryRadiusM: f.store.DeliveryRadiusM}
}

// --- geo_cache

func (f *fakeDB) GetGeoCache(_ context.Context, key string) (models.GeoCache, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.cache[key]
	if !ok {
		return c, pgx.ErrNoRows
	}
	return c, nil
}

func (f *fakeDB) UpsertGeoCacheGeocoded(_ context.Context, a models.UpsertGeoCacheGeocodedParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.cache[a.AddressKey]; ok && c.Pinned {
		return nil
	}
	f.cache[a.AddressKey] = models.GeoCache{AddressKey: a.AddressKey, Lat: a.Lat, Lng: a.Lng, Quality: a.Quality, Source: a.Source}
	return nil
}

func (f *fakeDB) UpsertGeoCachePinned(_ context.Context, a models.UpsertGeoCachePinnedParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cache[a.AddressKey] = models.GeoCache{AddressKey: a.AddressKey, Lat: a.Lat, Lng: a.Lng, Quality: QualityPinned, Source: "pin", Pinned: true}
	return nil
}

// --- order_geo

func (f *fakeDB) InsertOrderGeo(_ context.Context, a models.InsertOrderGeoParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := fromPg(a.OrderID)
	if _, ok := f.orderGeo[id]; ok {
		return 0, nil
	}
	st := f.stamp()
	f.orderGeo[id] = &models.OrderGeo{OrderID: a.OrderID, TenantID: a.TenantID, LocationID: a.LocationID, State: StatePending,
		NextTryAt: ts(f.now()), CreatedAt: ts(f.now()), UpdatedAt: st}
	return 1, nil
}

func (f *fakeDB) EnqueueMissingOrderGeo(context.Context, pgtype.Timestamptz) (int64, error) {
	return 0, nil
}

func (f *fakeDB) ListDueOrderGeo(_ context.Context, a models.ListDueOrderGeoParams) ([]models.ListDueOrderGeoRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.ListDueOrderGeoRow
	for _, g := range f.orderGeo {
		if !g.PushedAt.Valid && !g.NextTryAt.Time.After(a.Now.Time) {
			out = append(out, models.ListDueOrderGeoRow{OrderID: g.OrderID, TenantID: g.TenantID, LocationID: g.LocationID,
				State: g.State, Attempts: g.Attempts, CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt})
		}
	}
	return out, nil
}

func (f *fakeDB) GetOrderGeo(_ context.Context, id pgtype.UUID) (models.OrderGeo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.orderGeo[fromPg(id)]
	if !ok {
		return models.OrderGeo{}, pgx.ErrNoRows
	}
	return *g, nil
}

func (f *fakeDB) GetOrderForGeo(_ context.Context, a models.GetOrderForGeoParams) (models.GetOrderForGeoRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.orders[fromPg(a.OrderID)]
	if !ok || !f.inScope(a.TenantID, a.LocationID) {
		return o, pgx.ErrNoRows
	}
	return o, nil
}

func (f *fakeDB) SaveOrderGeoResult(_ context.Context, a models.SaveOrderGeoResultParams) (pgtype.Timestamptz, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.orderGeo[fromPg(a.OrderID)]
	if !ok {
		return pgtype.Timestamptz{}, pgx.ErrNoRows
	}
	g.State, g.Lat, g.Lng, g.Quality, g.Candidates = a.State, a.Lat, a.Lng, a.Quality, a.Candidates
	g.DistanceM, g.DurationS, g.Polyline, g.OutsideArea = a.DistanceM, a.DurationS, a.Polyline, a.OutsideArea
	g.StoreToS, g.ToStoreS, g.LastError = a.StoreToS, a.ToStoreS, a.LastError
	g.Corrected, g.Note = a.Corrected, ""
	g.Attempts++
	g.NextTryAt, g.PushedAt, g.UpdatedAt = ts(f.now()), pgtype.Timestamptz{}, f.stamp()
	return g.UpdatedAt, nil
}

func (f *fakeDB) SaveOrderGeoRetry(_ context.Context, a models.SaveOrderGeoRetryParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if g, ok := f.orderGeo[fromPg(a.OrderID)]; ok {
		g.Attempts++
		g.NextTryAt, g.LastError, g.UpdatedAt = a.NextTryAt, a.LastError, f.stamp()
	}
	return nil
}

func (f *fakeDB) MarkOrderGeoPushed(_ context.Context, a models.MarkOrderGeoPushedParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.orderGeo[fromPg(a.OrderID)]
	if !ok || !g.UpdatedAt.Time.Equal(a.UpdatedAt.Time) {
		return 0, nil
	}
	g.PushedAt, g.Confirmed = ts(f.now()), false
	return 1, nil
}

func (f *fakeDB) DeferOrderGeoPush(_ context.Context, a models.DeferOrderGeoPushParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if g, ok := f.orderGeo[fromPg(a.OrderID)]; ok {
		g.NextTryAt = a.NextTryAt
	}
	return nil
}

func (f *fakeDB) WakeLocationOrderGeo(context.Context, models.WakeLocationOrderGeoParams) (int64, error) {
	return 0, nil
}

func (f *fakeDB) ResetOrderGeoForChoice(_ context.Context, a models.ResetOrderGeoForChoiceParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.orderGeo[fromPg(a.OrderID)]
	g.State, g.Attempts, g.NextTryAt, g.LastError, g.PushedAt = StatePending, 0, ts(f.now()), "", pgtype.Timestamptz{}
	g.Candidates, g.ConfirmedAddress, g.Confirmed, g.UpdatedAt = nil, a.ConfirmedAddress, true, f.stamp()
	return nil
}

func (f *fakeDB) ResetOrderGeoForPin(_ context.Context, id pgtype.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.orderGeo[fromPg(id)]
	g.State, g.Attempts, g.NextTryAt, g.LastError, g.PushedAt = StatePending, 0, ts(f.now()), "", pgtype.Timestamptz{}
	g.Candidates, g.ConfirmedAddress, g.Confirmed, g.UpdatedAt = nil, nil, true, f.stamp()
	return nil
}

func (f *fakeDB) RequeueLocationOrderGeo(_ context.Context, a models.RequeueLocationOrderGeoParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, g := range f.orderGeo {
		if g.TenantID == a.TenantID && g.LocationID == a.LocationID && g.State != StatePending {
			g.State, g.Attempts, g.NextTryAt, g.PushedAt, g.Confirmed, g.UpdatedAt = StatePending, 0, ts(f.now()), pgtype.Timestamptz{}, false, f.stamp()
			n++
		}
	}
	return n, nil
}

func (f *fakeDB) ListOpenLocatedDeliveries(_ context.Context, a models.ListOpenLocatedDeliveriesParams) ([]models.ListOpenLocatedDeliveriesRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.ListOpenLocatedDeliveriesRow
	for id, g := range f.orderGeo {
		if g.State == StateLocated && g.Lat.Valid && id != fromPg(a.ExcludeID) {
			out = append(out, models.ListOpenLocatedDeliveriesRow{OrderID: g.OrderID, Lat: g.Lat, Lng: g.Lng})
		}
	}
	return out, nil
}

// --- external stops

func (f *fakeDB) UpsertExternalStop(_ context.Context, a models.UpsertExternalStopParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := fromPg(a.OrderID)
	if old, ok := f.stops[id]; ok && (old.TenantID != a.TenantID || old.LocationID != a.LocationID) {
		return 0, nil
	}
	st := f.stamp()
	f.stops[id] = &models.GeoExternalStop{OrderID: a.OrderID, TenantID: a.TenantID, LocationID: a.LocationID, State: StatePending,
		NextTryAt: ts(f.now()), CreatedAt: ts(f.now()), UpdatedAt: st, ExpiresAt: a.ExpiresAt}
	return 1, nil
}

func (f *fakeDB) UpsertExternalWork(_ context.Context, a models.UpsertExternalWorkParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.stops[fromPg(a.OrderID)]; !ok {
		return fmt.Errorf("fk: no stop")
	}
	f.work[fromPg(a.OrderID)] = &models.GeoExternalWork{OrderID: a.OrderID, Address: a.Address, AddressKey: a.AddressKey,
		CreatedAt: f.stamp(), DiscardAt: a.DiscardAt}
	return nil
}

func (f *fakeDB) GetExternalStop(_ context.Context, a models.GetExternalStopParams) (models.GeoExternalStop, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.stops[fromPg(a.OrderID)]
	if !ok || s.TenantID != a.TenantID || s.LocationID != a.LocationID {
		return models.GeoExternalStop{}, pgx.ErrNoRows
	}
	return *s, nil
}

func (f *fakeDB) GetExternalWork(_ context.Context, id pgtype.UUID) (models.GeoExternalWork, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.work[fromPg(id)]
	if !ok {
		return models.GeoExternalWork{}, pgx.ErrNoRows
	}
	return *w, nil
}

func (f *fakeDB) GetLocationGeo(_ context.Context, a models.GetLocationGeoParams) (models.GetLocationGeoRow, error) {
	if !f.inScope(a.TenantID, a.LocationID) {
		return models.GetLocationGeoRow{}, pgx.ErrNoRows
	}
	return f.store, nil
}

func (f *fakeDB) ListDueExternalStops(_ context.Context, a models.ListDueExternalStopsParams) ([]models.ListDueExternalStopsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.ListDueExternalStopsRow
	for _, s := range f.stops {
		if !s.PushedAt.Valid && !s.NextTryAt.Time.After(a.Now.Time) && s.ExpiresAt.Time.After(a.Now.Time) {
			out = append(out, models.ListDueExternalStopsRow{OrderID: s.OrderID, TenantID: s.TenantID, LocationID: s.LocationID,
				State: s.State, Attempts: s.Attempts, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt})
		}
	}
	return out, nil
}

func (f *fakeDB) SaveExternalStopResult(_ context.Context, a models.SaveExternalStopResultParams) (pgtype.Timestamptz, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.stops[fromPg(a.OrderID)]
	if !ok || !s.UpdatedAt.Time.Equal(a.ReadAt.Time) {
		return pgtype.Timestamptz{}, pgx.ErrNoRows
	}
	s.State, s.Lat, s.Lng, s.Quality = a.State, a.Lat, a.Lng, a.Quality
	s.DistanceM, s.DurationS, s.Polyline, s.OutsideArea = a.DistanceM, a.DurationS, a.Polyline, a.OutsideArea
	s.StoreToS, s.ToStoreS, s.LastError = a.StoreToS, a.ToStoreS, a.LastError
	s.Corrected, s.Note = a.Corrected, ""
	s.Attempts++
	s.NextTryAt, s.PushedAt, s.UpdatedAt = ts(f.now()), pgtype.Timestamptz{}, f.stamp()
	if hook := f.afterStopSave; hook != nil {
		f.afterStopSave = nil
		f.mu.Unlock()
		hook()
		f.mu.Lock()
	}
	return s.UpdatedAt, nil
}

func (f *fakeDB) MarkExternalStopPushed(_ context.Context, a models.MarkExternalStopPushedParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.stops[fromPg(a.OrderID)]
	if !ok || !s.UpdatedAt.Time.Equal(a.UpdatedAt.Time) {
		return 0, nil
	}
	s.PushedAt, s.Confirmed = ts(f.now()), false
	return 1, nil
}

func (f *fakeDB) DeferExternalStopPush(_ context.Context, a models.DeferExternalStopPushParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.stops[fromPg(a.OrderID)]; ok {
		s.NextTryAt = a.NextTryAt
	}
	return nil
}

func (f *fakeDB) WakeLocationExternalStops(_ context.Context, a models.WakeLocationExternalStopsParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wakes++
	var n int64
	for _, s := range f.stops {
		if s.TenantID == a.TenantID && s.LocationID == a.LocationID && !s.PushedAt.Valid && s.State != StatePending {
			s.NextTryAt = ts(f.now())
			n++
		}
	}
	return n, nil
}

func (f *fakeDB) SetExternalStopPoint(_ context.Context, a models.SetExternalStopPointParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.stops[fromPg(a.OrderID)]
	if !ok || s.TenantID != a.TenantID || s.LocationID != a.LocationID {
		return 0, nil
	}
	s.State, s.Lat, s.Lng, s.Quality = StatePending, a.Lat, a.Lng, a.Quality
	s.Attempts, s.NextTryAt, s.LastError, s.PushedAt, s.UpdatedAt = 0, ts(f.now()), "", pgtype.Timestamptz{}, f.stamp()
	s.Confirmed, s.Corrected, s.Note = true, false, ""
	return 1, nil
}

// --- 2.2.2 geo.search

func (f *fakeDB) SaveOrderGeoSearch(_ context.Context, a models.SaveOrderGeoSearchParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.orderGeo[fromPg(a.OrderID)]
	if !ok || g.TenantID != a.TenantID || g.LocationID != a.LocationID {
		return 0, nil
	}
	g.State, g.Lat, g.Lng, g.Quality, g.Candidates = a.State, pgtype.Float8{}, pgtype.Float8{}, "", a.Candidates
	g.ConfirmedAddress, g.Confirmed, g.Corrected, g.Note = nil, false, false, a.Note
	g.DistanceM, g.DurationS, g.Polyline, g.OutsideArea, g.StoreToS, g.ToStoreS = 0, 0, "", false, pgtype.Int4{}, pgtype.Int4{}
	g.NextTryAt, g.LastError, g.PushedAt, g.UpdatedAt = ts(f.now()), "", pgtype.Timestamptz{}, f.stamp()
	return 1, nil
}

func (f *fakeDB) SaveExternalStopSearch(_ context.Context, a models.SaveExternalStopSearchParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.stops[fromPg(a.OrderID)]
	if !ok || s.TenantID != a.TenantID || s.LocationID != a.LocationID {
		return 0, nil
	}
	s.State, s.Lat, s.Lng, s.Quality = a.State, pgtype.Float8{}, pgtype.Float8{}, ""
	s.Confirmed, s.Corrected, s.Note = false, false, a.Note
	s.DistanceM, s.DurationS, s.Polyline, s.OutsideArea, s.StoreToS, s.ToStoreS = 0, 0, "", false, pgtype.Int4{}, pgtype.Int4{}
	s.NextTryAt, s.LastError, s.PushedAt, s.UpdatedAt = ts(f.now()), "", pgtype.Timestamptz{}, f.stamp()
	return 1, nil
}

func (f *fakeDB) UpsertExternalWorkSearch(_ context.Context, a models.UpsertExternalWorkSearchParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := fromPg(a.OrderID)
	if _, ok := f.stops[id]; !ok {
		return fmt.Errorf("fk: no stop")
	}
	if w, ok := f.work[id]; ok {
		w.Candidates, w.ConfirmedAddress = a.Candidates, nil
		return nil
	}
	f.work[id] = &models.GeoExternalWork{OrderID: a.OrderID, Address: []byte(`{}`), Candidates: a.Candidates,
		CreatedAt: f.stamp(), DiscardAt: a.DiscardAt}
	return nil
}

func (f *fakeDB) RequeueLocationExternalStops(_ context.Context, a models.RequeueLocationExternalStopsParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, s := range f.stops {
		if s.TenantID == a.TenantID && s.LocationID == a.LocationID && s.State == StateLocated && s.Lat.Valid {
			s.State, s.Attempts, s.NextTryAt, s.PushedAt, s.Confirmed, s.UpdatedAt = StatePending, 0, ts(f.now()), pgtype.Timestamptz{}, false, f.stamp()
			n++
		}
	}
	return n, nil
}

func (f *fakeDB) SetExternalWorkCandidates(_ context.Context, a models.SetExternalWorkCandidatesParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if w, ok := f.work[fromPg(a.OrderID)]; ok && w.CreatedAt.Time.Equal(a.ReadAt.Time) {
		w.Candidates = a.Candidates
		return 1, nil
	}
	return 0, nil
}

func (f *fakeDB) SetExternalWorkConfirmed(_ context.Context, a models.SetExternalWorkConfirmedParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if w, ok := f.work[fromPg(a.OrderID)]; ok {
		w.Candidates, w.ConfirmedAddress = nil, a.ConfirmedAddress
	}
	return nil
}

func (f *fakeDB) DeleteExternalWork(_ context.Context, id pgtype.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.work, fromPg(id))
	return nil
}

func (f *fakeDB) DeleteExternalWorkRead(_ context.Context, a models.DeleteExternalWorkReadParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if w, ok := f.work[fromPg(a.OrderID)]; ok && w.CreatedAt.Time.Equal(a.ReadAt.Time) {
		delete(f.work, fromPg(a.OrderID))
		return 1, nil
	}
	return 0, nil
}

func (f *fakeDB) DeleteExternalWorkIfDone(_ context.Context, id pgtype.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.stops[fromPg(id)]; ok && s.PushedAt.Valid && s.State != StateCheck && s.State != StatePending {
		delete(f.work, fromPg(id))
	}
	return nil
}

func (f *fakeDB) DeleteExternalStop(_ context.Context, a models.DeleteExternalStopParams) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := fromPg(a.OrderID)
	s, ok := f.stops[id]
	if !ok || s.TenantID != a.TenantID || s.LocationID != a.LocationID {
		return 0, nil
	}
	delete(f.stops, id)
	delete(f.work, id) // ON DELETE CASCADE
	return 1, nil
}

func (f *fakeDB) PruneExternalStops(_ context.Context, now pgtype.Timestamptz) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for id, s := range f.stops {
		if !s.ExpiresAt.Time.After(now.Time) {
			delete(f.stops, id)
			delete(f.work, id)
			n++
		}
	}
	return n, nil
}

func (f *fakeDB) PruneExternalWork(_ context.Context, now pgtype.Timestamptz) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for id, w := range f.work {
		if !w.DiscardAt.Time.After(now.Time) {
			delete(f.work, id)
			n++
		}
	}
	return n, nil
}

func (f *fakeDB) ListLiveExternalStops(_ context.Context, a models.ListLiveExternalStopsParams) ([]models.ListLiveExternalStopsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []models.ListLiveExternalStopsRow
	for id, s := range f.stops {
		if s.State == StateLocated && s.Lat.Valid && id != fromPg(a.ExcludeID) && s.ExpiresAt.Time.After(f.now()) {
			out = append(out, models.ListLiveExternalStopsRow{OrderID: s.OrderID, Lat: s.Lat, Lng: s.Lng})
		}
	}
	return out, nil
}

// --- fakes around the service

type fakeStores struct{ loc tenancy.Location }

func (f fakeStores) GetLocation(context.Context, uuid.UUID) (*tenancy.Location, error) {
	l := f.loc
	return &l, nil
}
func (fakeStores) SetLocationGeocoded(context.Context, uuid.UUID, float64, float64) (bool, error) {
	return false, nil
}
func (fakeStores) ListLocationsNeedingGeocode(context.Context) ([]tenancy.Location, error) {
	return nil, nil
}

// fakeAgent answers order.geo pushes (ok) and records them.
type fakeAgent struct {
	mu      sync.Mutex
	offline bool
	pushes  []Push
}

func (a *fakeAgent) Request(_ context.Context, _ sseBroker.Scope, msgType string, payload any, _ time.Duration) (rpc.Envelope, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.offline {
		return rpc.Envelope{}, rpc.ErrAgentOffline
	}
	if msgType != rpc.MsgOrderGeo {
		return rpc.Envelope{}, fmt.Errorf("unexpected %s", msgType)
	}
	a.pushes = append(a.pushes, payload.(Push))
	return rpc.Envelope{Type: msgType + ".reply", Payload: json.RawMessage(`{"ok":true}`)}, nil
}

func (a *fakeAgent) take() []Push {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.pushes
	a.pushes = nil
	return p
}

// fakeProvider: geocodes from a table keyed by the lowercased street line
// (missing = no match) and drives at 10 m/s in a straight line.
type fakeProvider struct {
	mu      sync.Mutex
	known   map[string][]Candidate
	geocode int
}

func (p *fakeProvider) Geocode(_ context.Context, a Address, _ *Point) (GeocodeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.geocode++
	cs := p.known[strings.ToLower(a.Line1)]
	return resultFrom(cs, "fake")
}

func (p *fakeProvider) Route(_ context.Context, from, to Point) (RouteResult, error) {
	d := HaversineM(from, to)
	return RouteResult{DistanceM: int(d * 1.3), DurationS: int(d / 10), Polyline: "fake"}, nil
}

func (p *fakeProvider) Table(_ context.Context, origins, dests []Point) ([][]int, error) {
	out := make([][]int, len(origins))
	for i, o := range origins {
		out[i] = make([]int, len(dests))
		for j, d := range dests {
			out[i][j] = int(math.Round(HaversineM(o, d) / 10))
		}
	}
	return out, nil
}

func (p *fakeProvider) Trip(context.Context, Point, []Point) (TripResult, error) {
	return TripResult{}, fmt.Errorf("not used")
}

type harness struct {
	t      *testing.T
	svc    *Service
	db     *fakeDB
	agent  *fakeAgent
	prov   *fakeProvider
	stores *fakeStores
	scope  sseBroker.Scope
	clock  time.Time
}

var testStore = Point{Lat: 39.3982, Lng: -76.5895} // Lombardi's, Towson

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, clock: time.Date(2026, 10, 25, 18, 0, 0, 0, time.UTC),
		scope: sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}}
	now := func() time.Time { return h.clock }
	h.db = newFakeDB(now, h.scope, testStore)
	h.agent = &fakeAgent{}
	h.prov = &fakeProvider{known: map[string][]Candidate{}}
	lat, lng := testStore.Lat, testStore.Lng
	h.stores = &fakeStores{loc: tenancy.Location{ID: h.scope.LocationID, TenantID: h.scope.TenantID, Lat: &lat, Lng: &lng, DeliveryRadiusM: 8047,
		AddressLine1: "931 Taylor Ave", AddressLine2: "Towson, MD 21286"}}
	stores := h.stores
	h.svc = newService(h.db, func(ctx context.Context, fn func(Queries) error) error { return fn(h.db) },
		stores, h.agent, Config{}, zerolog.Nop())
	h.svc.now = now
	h.svc.searches.SetClock(now)
	h.svc.spawn = func(f func()) { f() }
	h.svc.prov.Store(&Providers{Geocoder: h.prov, Router: h.prov, name: "fake", routing: true})
	return h
}

// drain works every due row (orders + stops) synchronously until none is due.
func (h *harness) drain() {
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		rows, _ := h.db.ListDueOrderGeo(ctx, models.ListDueOrderGeoParams{Now: ts(h.clock), MaxRows: 50})
		stops, _ := h.db.ListDueExternalStops(ctx, models.ListDueExternalStopsParams{Now: ts(h.clock), MaxRows: 50})
		if len(rows)+len(stops) == 0 {
			return
		}
		for _, r := range rows {
			h.svc.process(ctx, r)
		}
		for _, r := range stops {
			h.svc.processStop(ctx, r)
		}
	}
	h.t.Fatal("drain: rows still due after 20 rounds")
}

// rpc calls an inbound handler and returns its reply as a map.
func (h *harness) rpc(fn func(context.Context, sseBroker.Scope, json.RawMessage) (any, error), payload any) map[string]any {
	h.t.Helper()
	raw, _ := json.Marshal(payload)
	out, err := fn(context.Background(), h.scope, raw)
	if err != nil {
		h.t.Fatalf("rpc: %v", err)
	}
	b, _ := json.Marshal(out)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func (h *harness) knows(line1 string, cs ...Candidate) { h.prov.known[strings.ToLower(line1)] = cs }

func cand(line1, city, zip string, lat, lng float64) Candidate {
	return newCandidate(line1, city, "MD", zip, Point{Lat: lat, Lng: lng}, QualityExact)
}
