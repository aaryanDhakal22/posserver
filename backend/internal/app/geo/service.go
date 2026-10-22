package geo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tenancyApp "quiccpos/main/internal/app/tenancy"
	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/infra/database/models"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/observability"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Requester is the slice of *rpc.Requester the service uses (main → agent).
type Requester interface {
	Request(ctx context.Context, scope sseBroker.Scope, msgType string, payload any, timeout time.Duration) (rpc.Envelope, error)
}

const (
	// openWindow bounds "open delivery" + the ingest safety sweep to the
	// current service (orders received in the last 12 h).
	openWindow = 12 * time.Hour
	// maxTableOthers caps the drive-time table (OSRM's default table limit is 100).
	maxTableOthers = 50
	// routeRetries: a routing failure is retried this many times before the
	// order is located without a route (a map pin beats no map).
	routeRetries = 3
	workers      = 6
)

// Service is the geo worker + the agent-facing geo RPCs + store geocoding.
type Service struct {
	pool    *pgxpool.Pool
	tenancy *tenancyApp.Service
	req     Requester
	logger  zerolog.Logger

	base Config
	prov atomic.Pointer[Providers]

	storeChanged func(ctx context.Context, tenantID, locationID uuid.UUID)

	wake     chan struct{}
	sem      chan struct{}
	mu       sync.Mutex
	inflight map[uuid.UUID]bool

	// PushTimeout is how long an order.geo push waits for the agent's reply.
	PushTimeout time.Duration
	// ConnectDelay lets a (re)connecting agent store its order backlog before
	// the unpushed geo rows follow.
	ConnectDelay time.Duration

	located metric.Int64Counter
	pushes  metric.Int64Counter
}

// New builds the service with the providers chosen by cfg.
func New(pool *pgxpool.Pool, tenancy *tenancyApp.Service, req Requester, cfg Config, logger zerolog.Logger) *Service {
	m := otel.Meter(observability.MeterName)
	s := &Service{
		pool: pool, tenancy: tenancy, req: req, base: cfg,
		logger:       logger.With().Str("module", "geo").Logger(),
		wake:         make(chan struct{}, 1),
		sem:          make(chan struct{}, workers),
		inflight:     map[uuid.UUID]bool{},
		PushTimeout:  5 * time.Second,
		ConnectDelay: 3 * time.Second,
	}
	s.located, _ = m.Int64Counter("geo.orders", metric.WithDescription("Delivery orders worked by the geo worker, by result (located / not_found / retry)"))
	s.pushes, _ = m.Int64Counter("geo.pushes", metric.WithDescription("order.geo pushes to an agent, by result"))
	s.prov.Store(Build(cfg))
	return s
}

// Providers returns the active providers.
func (s *Service) Providers() *Providers { return s.prov.Load() }

// SetProviders swaps the providers at runtime (dev-only test override, see
// handler/geo.go); ResetProviders restores the env configuration.
func (s *Service) SetProviders(c Config) { s.prov.Store(Build(c)) }

// ResetProviders restores the providers built from the env.
func (s *Service) ResetProviders() { s.prov.Store(Build(s.base)) }

// OnStoreChanged registers the store_config push (cmd/api wires the
// StoreConfigPublisher; this package can't import transport).
func (s *Service) OnStoreChanged(fn func(ctx context.Context, tenantID, locationID uuid.UUID)) {
	s.storeChanged = fn
}

func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func pgUUID(u uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: u, Valid: true} }
func ts(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// --- enqueue ---------------------------------------------------------------------

// OnOrderCreated enqueues a freshly persisted delivery order (order service
// hook). Never fails ingest: an error is logged and the worker's sweep picks
// the order up within a minute.
func (s *Service) OnOrderCreated(ctx context.Context, o *order.Order) {
	if o.Fulfillment().Type != order.FulfillmentDelivery {
		return
	}
	if _, err := models.New(s.pool).InsertOrderGeo(ctx, models.InsertOrderGeoParams{
		OrderID: pgUUID(uuid.UUID(o.ID())), TenantID: pgUUID(uuid.UUID(o.TenantID())), LocationID: pgUUID(uuid.UUID(o.LocationID())),
	}); err != nil {
		s.logger.Warn().Ctx(ctx).Err(err).Str("order_id", uuid.UUID(o.ID()).String()).Msg("geo enqueue failed (sweep will retry)")
		return
	}
	s.poke()
}

// WakeLocation re-pushes everything a (re)connected agent has not acked
// (broker subscribe hook). Runs after ConnectDelay, off the caller's path.
func (s *Service) WakeLocation(scope sseBroker.Scope) {
	go func() {
		time.Sleep(s.ConnectDelay)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		n, err := models.New(s.pool).WakeLocationOrderGeo(ctx, models.WakeLocationOrderGeoParams{
			TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID),
		})
		if err != nil {
			s.logger.Warn().Err(err).Msg("geo: wake location failed")
			return
		}
		if n > 0 {
			s.logger.Info().Str("location_id", scope.LocationID.String()).Int64("rows", n).Msg("geo: re-pushing unacked results to the reconnected agent")
			s.poke()
		}
	}()
}

// --- worker ------------------------------------------------------------------------

// Run works due order_geo rows until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	var lastSweep time.Time
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
		q := models.New(s.pool)
		now := time.Now()
		if now.Sub(lastSweep) > time.Minute {
			lastSweep = now
			if n, err := q.EnqueueMissingOrderGeo(ctx, ts(now.Add(-openWindow))); err != nil {
				if ctx.Err() == nil {
					s.logger.Warn().Err(err).Msg("geo: sweep failed")
				}
			} else if n > 0 {
				s.logger.Info().Int64("rows", n).Msg("geo: sweep enqueued missed delivery orders")
			}
		}
		rows, err := q.ListDueOrderGeo(ctx, models.ListDueOrderGeoParams{Now: ts(now), MaxRows: 50})
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn().Err(err).Msg("geo: list due failed")
			}
			continue
		}
		for _, r := range rows {
			id := uuid.UUID(r.OrderID.Bytes)
			s.mu.Lock()
			busy := s.inflight[id]
			if !busy {
				s.inflight[id] = true
			}
			s.mu.Unlock()
			if busy {
				continue
			}
			select {
			case s.sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			wg.Add(1)
			go func(r models.ListDueOrderGeoRow) {
				defer wg.Done()
				defer func() {
					<-s.sem
					s.mu.Lock()
					delete(s.inflight, uuid.UUID(r.OrderID.Bytes))
					s.mu.Unlock()
				}()
				s.process(ctx, r)
			}(r)
		}
	}
}

func (s *Service) process(ctx context.Context, r models.ListDueOrderGeoRow) {
	id := uuid.UUID(r.OrderID.Bytes)
	scope := sseBroker.Scope{TenantID: uuid.UUID(r.TenantID.Bytes), LocationID: uuid.UUID(r.LocationID.Bytes)}
	log := s.logger.With().Str("order_id", id.String()).Logger()
	if r.State == StatePending {
		done, err := s.locate(ctx, scope, id, int(r.Attempts), r.CreatedAt.Time)
		if err != nil {
			if ctx.Err() == nil {
				log.Warn().Err(err).Msg("geo: locate failed (db)")
				_ = models.New(s.pool).DeferOrderGeoPush(ctx, models.DeferOrderGeoPushParams{OrderID: r.OrderID, NextTryAt: ts(time.Now().Add(30 * time.Second))})
			}
			return
		}
		if !done {
			return // scheduled a retry
		}
	}
	if err := s.push(ctx, scope, id); err != nil && ctx.Err() == nil {
		log.Warn().Err(err).Msg("geo: push failed (db)")
	}
}

// orderAddress decodes the stored fulfillment address (order.Address JSON).
func orderAddress(raw []byte) (order.Address, bool) {
	var a order.Address
	if len(raw) == 0 || json.Unmarshal(raw, &a) != nil {
		return a, false
	}
	return a, strings.TrimSpace(a.Line1) != ""
}

// cacheKey is the geo_cache key for an order: its normalized address, or
// (no street address) the order itself, so a pin still sticks to it.
func cacheKey(id uuid.UUID, a order.Address, ok bool) string {
	if !ok {
		return "order|" + id.String()
	}
	return NormalizeKey(Address{Line1: a.Line1, City: a.City, State: a.State, Zip: a.PostalCode})
}

type located struct {
	state, quality, lastErr string
	pt                      *Point
	route                   RouteResult
	outside                 bool
	storeTo, toStore        *int
}

// locate works one pending row. done=false means a retry was scheduled.
func (s *Service) locate(ctx context.Context, scope sseBroker.Scope, id uuid.UUID, attempts int, created time.Time) (bool, error) {
	q := models.New(s.pool)
	o, err := q.GetOrderForGeo(ctx, models.GetOrderForGeoParams{OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, q.DeferOrderGeoPush(ctx, models.DeferOrderGeoPushParams{OrderID: pgUUID(id), NextTryAt: ts(time.Now().Add(time.Hour))})
	}
	if err != nil {
		return false, err
	}
	prov := s.Providers()
	addr, hasAddr := orderAddress(o.FulfillmentAddress)
	var store *Point
	if o.StoreLat.Valid && o.StoreLng.Valid {
		store = &Point{Lat: o.StoreLat.Float64, Lng: o.StoreLng.Float64}
	}
	res := located{state: StateNotFound}

	retry := func(cause error) (bool, error) {
		next, ok := NextTry(created, time.Now(), attempts+1)
		if !ok {
			return true, nil // caller saves the final result
		}
		s.located.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "retry")))
		s.logger.Info().Ctx(ctx).Err(cause).Str("order_id", id.String()).Time("next_try_at", next).Msg("geo: provider failure, will retry")
		return false, q.SaveOrderGeoRetry(ctx, models.SaveOrderGeoRetryParams{OrderID: pgUUID(id), NextTryAt: ts(next), LastError: trunc(cause.Error())})
	}

	key := cacheKey(id, addr, hasAddr)
	cached, err := q.GetGeoCache(ctx, key)
	switch {
	case err == nil:
		pt := Point{Lat: cached.Lat, Lng: cached.Lng}
		res.pt, res.quality = &pt, cached.Quality
		if cached.Pinned {
			res.quality = QualityPinned
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return false, err
	case !hasAddr:
		res.lastErr = "no delivery address"
	default:
		a := Address{Line1: addr.Line1, City: addr.City, State: addr.State, Zip: addr.PostalCode}
		g, gerr := prov.Geocode(ctx, a, store)
		switch {
		case gerr == nil:
			res.pt, res.quality = &g.Point, g.Quality
			if err := q.UpsertGeoCacheGeocoded(ctx, models.UpsertGeoCacheGeocodedParams{
				AddressKey: key, Lat: g.Lat, Lng: g.Lng, Quality: g.Quality, Source: g.Source,
			}); err != nil {
				return false, err
			}
		case errors.Is(gerr, ErrNotFound):
			res.lastErr = "no geocoder match"
		default:
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			if done, err := retry(gerr); !done || err != nil {
				return done, err
			}
			res.lastErr = "gave up: " + gerr.Error()
		}
	}

	if res.pt != nil {
		res.state = StateLocated
		if store != nil {
			res.outside = OutsideArea(*store, *res.pt, int(o.DeliveryRadiusM))
			if prov.Routing() {
				rt, rerr := prov.Route(ctx, *store, *res.pt)
				switch {
				case rerr == nil:
					res.route = rt
					d := rt.DurationS
					res.storeTo = &d
				case errors.Is(rerr, ErrNoRoute):
					res.lastErr = "no route from the store"
				default:
					if ctx.Err() != nil {
						return false, ctx.Err()
					}
					if attempts+1 < routeRetries {
						if done, err := retry(rerr); !done || err != nil {
							return done, err
						}
					}
					res.lastErr = "located without a route: " + rerr.Error()
				}
				if tbl, terr := prov.Table(ctx, []Point{*res.pt}, []Point{*store}); terr == nil && len(tbl) == 1 && len(tbl[0]) == 1 && tbl[0][0] >= 0 {
					v := tbl[0][0]
					res.toStore = &v
				}
			}
		}
	}
	if err := s.saveResult(ctx, id, res); err != nil {
		return false, err
	}
	s.located.Add(ctx, 1, metric.WithAttributes(attribute.String("result", res.state)))
	s.logger.Info().Ctx(ctx).Str("order_id", id.String()).Str("state", res.state).Str("quality", res.quality).
		Bool("outside_area", res.outside).Str("note", res.lastErr).Msg("geo: order located")
	return true, nil
}

func (s *Service) saveResult(ctx context.Context, id uuid.UUID, r located) error {
	p := models.SaveOrderGeoResultParams{
		OrderID: pgUUID(id), State: r.state, Quality: r.quality, DistanceM: int32(r.route.DistanceM),
		DurationS: int32(r.route.DurationS), Polyline: r.route.Polyline, OutsideArea: r.outside, LastError: trunc(r.lastErr),
	}
	if r.pt != nil {
		p.Lat, p.Lng = pgtype.Float8{Float64: r.pt.Lat, Valid: true}, pgtype.Float8{Float64: r.pt.Lng, Valid: true}
	}
	if r.storeTo != nil {
		p.StoreToS = pgtype.Int4{Int32: int32(*r.storeTo), Valid: true}
	}
	if r.toStore != nil {
		p.ToStoreS = pgtype.Int4{Int32: int32(*r.toStore), Valid: true}
	}
	_, err := models.New(s.pool).SaveOrderGeoResult(ctx, p)
	return err
}

func trunc(s string) string {
	if len(s) > 500 {
		return s[:500]
	}
	return s
}

// --- push ----------------------------------------------------------------------------

// TimePair is one drive-time pair of order.geo: ToS = this → other,
// FromS = other → this.
type TimePair struct {
	Other string `json:"other"`
	ToS   int    `json:"to_s"`
	FromS int    `json:"from_s"`
}

// Push is the order.geo payload (contract/agent.md, main → agent).
type Push struct {
	OrderID     string     `json:"order_id"`
	State       string     `json:"state"`
	Lat         *float64   `json:"lat,omitempty"`
	Lng         *float64   `json:"lng,omitempty"`
	Quality     string     `json:"quality,omitempty"`
	DistanceM   int        `json:"distance_m"`
	DurationS   int        `json:"duration_s"`
	Polyline    string     `json:"polyline"`
	OutsideArea bool       `json:"outside_area"`
	StoreToS    *int       `json:"store_to_s,omitempty"`
	ToStoreS    *int       `json:"to_store_s,omitempty"`
	Times       []TimePair `json:"times,omitempty"`
}

func (s *Service) push(ctx context.Context, scope sseBroker.Scope, id uuid.UUID) error {
	q := models.New(s.pool)
	g, err := q.GetOrderGeo(ctx, pgUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if g.State == StatePending || g.PushedAt.Valid {
		return nil
	}
	p := Push{OrderID: id.String(), State: g.State, DistanceM: int(g.DistanceM), DurationS: int(g.DurationS),
		Polyline: g.Polyline, OutsideArea: g.OutsideArea}
	if g.State == StateLocated && g.Lat.Valid && g.Lng.Valid {
		lat, lng := g.Lat.Float64, g.Lng.Float64
		p.Lat, p.Lng, p.Quality = &lat, &lng, g.Quality
		if g.StoreToS.Valid {
			v := int(g.StoreToS.Int32)
			p.StoreToS = &v
		}
		if g.ToStoreS.Valid {
			v := int(g.ToStoreS.Int32)
			p.ToStoreS = &v
		}
		s.addTimes(ctx, scope, id, Point{Lat: lat, Lng: lng}, &p)
	}

	stale := time.Since(g.CreatedAt.Time) > openWindow
	deferFor := func(d time.Duration) error {
		return q.DeferOrderGeoPush(ctx, models.DeferOrderGeoPushParams{OrderID: pgUUID(id), NextTryAt: ts(time.Now().Add(d))})
	}
	markPushed := func() error {
		_, err := q.MarkOrderGeoPushed(ctx, models.MarkOrderGeoPushedParams{OrderID: pgUUID(id), UpdatedAt: g.UpdatedAt})
		return err
	}
	result := func(r string) { s.pushes.Add(ctx, 1, metric.WithAttributes(attribute.String("result", r))) }

	env, err := s.req.Request(ctx, scope, rpc.MsgOrderGeo, p, s.PushTimeout)
	switch {
	case errors.Is(err, rpc.ErrAgentOffline):
		result("offline")
		if stale {
			return markPushed()
		}
		// The agent's reconnect wakes these rows (WakeLocation).
		return deferFor(10 * time.Minute)
	case errors.Is(err, rpc.ErrTimeout):
		result("timeout")
		if stale {
			return markPushed()
		}
		// An agent older than 2.0.9 never answers order.geo.
		return deferFor(5 * time.Minute)
	case err != nil:
		result("error")
		return deferFor(time.Minute)
	}
	var reply struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}
	_ = json.Unmarshal(env.Payload, &reply)
	if reply.OK {
		result("ok")
		return markPushed()
	}
	result("rejected_" + reply.Code)
	if stale {
		return markPushed()
	}
	// not_found: the agent has not stored the order yet (backlog) — soon.
	return deferFor(30 * time.Second)
}

// addTimes fills the drive times between this order, the store and every
// other open located delivery of the location (best effort: a routing
// failure just leaves times out).
func (s *Service) addTimes(ctx context.Context, scope sseBroker.Scope, id uuid.UUID, pt Point, p *Push) {
	prov := s.Providers()
	if !prov.Routing() {
		return
	}
	loc, err := s.tenancy.GetLocation(ctx, scope.LocationID)
	if err != nil || loc.Lat == nil || loc.Lng == nil {
		return
	}
	others, err := models.New(s.pool).ListOpenLocatedDeliveries(ctx, models.ListOpenLocatedDeliveriesParams{
		TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID), ExcludeID: pgUUID(id),
		Since: ts(time.Now().Add(-openWindow)), MaxRows: maxTableOthers,
	})
	if err != nil {
		s.logger.Warn().Ctx(ctx).Err(err).Msg("geo: list open deliveries failed")
		return
	}
	pts := []Point{{Lat: *loc.Lat, Lng: *loc.Lng}}
	for _, o := range others {
		pts = append(pts, Point{Lat: o.Lat.Float64, Lng: o.Lng.Float64})
	}
	out, err1 := prov.Table(ctx, []Point{pt}, pts)
	in, err2 := prov.Table(ctx, pts, []Point{pt})
	if err1 != nil || err2 != nil || len(out) != 1 || len(out[0]) != len(pts) || len(in) != len(pts) {
		s.logger.Info().Ctx(ctx).AnErr("out", err1).AnErr("in", err2).Msg("geo: drive-time table unavailable; pushing without times")
		return
	}
	if v := out[0][0]; v >= 0 {
		p.ToStoreS = &v
	}
	if v := in[0][0]; v >= 0 {
		p.StoreToS = &v
	}
	p.Times = []TimePair{}
	for j, o := range others {
		to, from := out[0][j+1], in[j+1][0]
		if to < 0 || from < 0 {
			continue
		}
		p.Times = append(p.Times, TimePair{Other: uuid.UUID(o.OrderID.Bytes).String(), ToS: to, FromS: from})
	}
}

// --- agent → main RPCs ------------------------------------------------------------

// HandlePin is the rpc.InboundHandler for geo.pin {order_id, lat, lng}: staff
// dropped a pin for an order on the tablet. Saved pinned in geo_cache for the
// order's normalized address (so the next order there lands right), then the
// order is re-worked and order.geo pushed again with quality pinned.
func (s *Service) HandlePin(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b struct {
		OrderID string   `json:"order_id"`
		Lat     *float64 `json:"lat"`
		Lng     *float64 `json:"lng"`
	}
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("%w: geo.pin: %v", rpc.ErrBadPayload, err)
	}
	id, err := uuid.Parse(b.OrderID)
	if err != nil || b.Lat == nil || b.Lng == nil || *b.Lat < -90 || *b.Lat > 90 || *b.Lng < -180 || *b.Lng > 180 {
		return nil, fmt.Errorf("%w: geo.pin needs {order_id, lat, lng}", rpc.ErrBadPayload)
	}
	q := models.New(s.pool)
	o, err := q.GetOrderForGeo(ctx, models.GetOrderForGeoParams{OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]any{"ok": false, "code": "not_found"}, nil
	}
	if err != nil {
		return nil, err
	}
	addr, ok := orderAddress(o.FulfillmentAddress)
	if err := q.UpsertGeoCachePinned(ctx, models.UpsertGeoCachePinnedParams{AddressKey: cacheKey(id, addr, ok), Lat: *b.Lat, Lng: *b.Lng}); err != nil {
		return nil, err
	}
	if _, err := q.InsertOrderGeo(ctx, models.InsertOrderGeoParams{OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID)}); err != nil {
		return nil, err
	}
	if err := q.ResetOrderGeoForPin(ctx, pgUUID(id)); err != nil {
		return nil, err
	}
	s.logger.Info().Ctx(ctx).Str("order_id", id.String()).Msg("geo: pin saved for the order's address")
	s.poke()
	return map[string]any{"ok": true}, nil
}

// HandleTrip is the rpc.InboundHandler for geo.trip {order_ids}: the fastest
// driving order from the store through those orders (no return leg). Located
// orders are ordered by the router; any not located follow at the end.
func (s *Service) HandleTrip(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b struct {
		OrderIDs []string `json:"order_ids"`
	}
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("%w: geo.trip: %v", rpc.ErrBadPayload, err)
	}
	if len(b.OrderIDs) == 0 || len(b.OrderIDs) > 25 {
		return nil, fmt.Errorf("%w: geo.trip needs 1..25 order_ids", rpc.ErrBadPayload)
	}
	ids := make([]pgtype.UUID, 0, len(b.OrderIDs))
	for _, raw := range b.OrderIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: geo.trip: bad order id %q", rpc.ErrBadPayload, raw)
		}
		ids = append(ids, pgUUID(id))
	}
	fail := func(code string) (any, error) {
		return map[string]any{"ok": false, "code": code, "order_ids": b.OrderIDs}, nil
	}
	loc, err := s.tenancy.GetLocation(ctx, scope.LocationID)
	if err != nil {
		return nil, err
	}
	if loc.Lat == nil || loc.Lng == nil {
		return fail("no_store_location")
	}
	prov := s.Providers()
	if !prov.Routing() {
		return fail("routing_disabled")
	}
	rows, err := models.New(s.pool).ListOrderGeoByIDs(ctx, models.ListOrderGeoByIDsParams{
		TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID), Ids: ids,
	})
	if err != nil {
		return nil, err
	}
	at := map[string]Point{}
	for _, r := range rows {
		if r.State == StateLocated && r.Lat.Valid && r.Lng.Valid {
			at[uuid.UUID(r.OrderID.Bytes).String()] = Point{Lat: r.Lat.Float64, Lng: r.Lng.Float64}
		}
	}
	var stops []Point
	var stopIDs, unlocated []string
	seen := map[string]bool{}
	for _, raw := range b.OrderIDs {
		id := strings.ToLower(raw)
		if seen[id] {
			continue
		}
		seen[id] = true
		if p, ok := at[id]; ok {
			stops, stopIDs = append(stops, p), append(stopIDs, id)
		} else {
			unlocated = append(unlocated, id)
		}
	}
	out := map[string]any{"ok": true, "polyline": "", "distance_m": 0, "duration_s": 0, "unlocated": nonNil(unlocated)}
	if len(stops) == 0 {
		out["order_ids"] = nonNil(unlocated)
		return out, nil
	}
	trip, err := prov.Trip(ctx, Point{Lat: *loc.Lat, Lng: *loc.Lng}, stops)
	if err != nil {
		s.logger.Warn().Ctx(ctx).Err(err).Msg("geo.trip: routing failed")
		return fail("routing_failed")
	}
	ordered := make([]string, 0, len(b.OrderIDs))
	for _, i := range trip.Order {
		ordered = append(ordered, stopIDs[i])
	}
	out["order_ids"] = append(ordered, unlocated...)
	out["polyline"], out["distance_m"], out["duration_s"] = trip.Polyline, trip.DistanceM, trip.DurationS
	return out, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// --- store position --------------------------------------------------------------

// GeocodeStore geocodes a location's address unless its pin was placed by
// hand, then pushes store_config and re-routes its open deliveries when the
// position changed.
func (s *Service) GeocodeStore(ctx context.Context, locationID uuid.UUID) error {
	l, err := s.tenancy.GetLocation(ctx, locationID)
	if err != nil {
		return err
	}
	if l.GeoSource == tenancy.GeoSourceManual || strings.TrimSpace(l.AddressLine1) == "" {
		return nil
	}
	g, err := s.Providers().Geocode(ctx, StoreAddress(l.AddressLine1, l.AddressLine2), nil)
	if err != nil {
		return err
	}
	if l.Lat != nil && l.Lng != nil && *l.Lat == g.Lat && *l.Lng == g.Lng {
		return nil
	}
	ok, err := s.tenancy.SetLocationGeocoded(ctx, locationID, g.Lat, g.Lng)
	if err != nil || !ok {
		return err
	}
	s.logger.Info().Ctx(ctx).Str("location_id", locationID.String()).Str("quality", g.Quality).Str("source", g.Source).Msg("geo: store address geocoded")
	s.StoreMoved(ctx, l.TenantID, locationID)
	return nil
}

// GeocodeStoreAsync is GeocodeStore off the request path (location save).
func (s *Service) GeocodeStoreAsync(locationID uuid.UUID) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.GeocodeStore(ctx, locationID); err != nil {
			s.logger.Warn().Err(err).Str("location_id", locationID.String()).Msg("geo: store geocode failed")
		}
	}()
}

// StoreMoved pushes the new store_config and re-works the location's open
// deliveries (distances + outside_area are from the store). Their addresses
// are cached, so this is routing only.
func (s *Service) StoreMoved(ctx context.Context, tenantID, locationID uuid.UUID) {
	if s.storeChanged != nil {
		s.storeChanged(ctx, tenantID, locationID)
	}
	n, err := models.New(s.pool).RequeueLocationOrderGeo(ctx, models.RequeueLocationOrderGeoParams{
		TenantID: pgUUID(tenantID), LocationID: pgUUID(locationID), Since: ts(time.Now().Add(-openWindow)),
	})
	if err != nil {
		s.logger.Warn().Ctx(ctx).Err(err).Msg("geo: requeue open deliveries failed")
		return
	}
	if n > 0 {
		s.poke()
	}
}

// BackfillStores geocodes every store with an address but no position (once
// at startup; sequential, Nominatim is rate-limited).
func (s *Service) BackfillStores(ctx context.Context) {
	locs, err := s.tenancy.ListLocationsNeedingGeocode(ctx)
	if err != nil {
		s.logger.Warn().Err(err).Msg("geo: store backfill list failed")
		return
	}
	for _, l := range locs {
		if ctx.Err() != nil {
			return
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := s.GeocodeStore(cctx, l.ID); err != nil {
			s.logger.Warn().Err(err).Str("location_id", l.ID.String()).Msg("geo: store backfill geocode failed")
		}
		cancel()
	}
}
