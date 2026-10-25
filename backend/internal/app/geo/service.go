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

// Stores is the slice of *tenancy.Service the service uses.
type Stores interface {
	GetLocation(ctx context.Context, id uuid.UUID) (*tenancy.Location, error)
	SetLocationGeocoded(ctx context.Context, id uuid.UUID, lat, lng float64) (bool, error)
	ListLocationsNeedingGeocode(ctx context.Context) ([]tenancy.Location, error)
}

// Queries is the slice of the sqlc querier the service uses (a fake in tests).
type Queries interface {
	GetGeoCache(ctx context.Context, addressKey string) (models.GeoCache, error)
	UpsertGeoCacheGeocoded(ctx context.Context, arg models.UpsertGeoCacheGeocodedParams) error
	UpsertGeoCachePinned(ctx context.Context, arg models.UpsertGeoCachePinnedParams) error

	InsertOrderGeo(ctx context.Context, arg models.InsertOrderGeoParams) (int64, error)
	EnqueueMissingOrderGeo(ctx context.Context, since pgtype.Timestamptz) (int64, error)
	ListDueOrderGeo(ctx context.Context, arg models.ListDueOrderGeoParams) ([]models.ListDueOrderGeoRow, error)
	GetOrderGeo(ctx context.Context, orderID pgtype.UUID) (models.OrderGeo, error)
	GetOrderForGeo(ctx context.Context, arg models.GetOrderForGeoParams) (models.GetOrderForGeoRow, error)
	SaveOrderGeoResult(ctx context.Context, arg models.SaveOrderGeoResultParams) (pgtype.Timestamptz, error)
	SaveOrderGeoRetry(ctx context.Context, arg models.SaveOrderGeoRetryParams) error
	MarkOrderGeoPushed(ctx context.Context, arg models.MarkOrderGeoPushedParams) (int64, error)
	DeferOrderGeoPush(ctx context.Context, arg models.DeferOrderGeoPushParams) error
	WakeLocationOrderGeo(ctx context.Context, arg models.WakeLocationOrderGeoParams) (int64, error)
	ResetOrderGeoForPin(ctx context.Context, orderID pgtype.UUID) error
	ResetOrderGeoForChoice(ctx context.Context, arg models.ResetOrderGeoForChoiceParams) error
	RequeueLocationOrderGeo(ctx context.Context, arg models.RequeueLocationOrderGeoParams) (int64, error)
	ListOpenLocatedDeliveries(ctx context.Context, arg models.ListOpenLocatedDeliveriesParams) ([]models.ListOpenLocatedDeliveriesRow, error)
	ListOrderGeoByIDs(ctx context.Context, arg models.ListOrderGeoByIDsParams) ([]models.ListOrderGeoByIDsRow, error)

	UpsertExternalStop(ctx context.Context, arg models.UpsertExternalStopParams) (int64, error)
	UpsertExternalWork(ctx context.Context, arg models.UpsertExternalWorkParams) error
	GetExternalStop(ctx context.Context, arg models.GetExternalStopParams) (models.GeoExternalStop, error)
	GetExternalWork(ctx context.Context, orderID pgtype.UUID) (models.GeoExternalWork, error)
	GetLocationGeo(ctx context.Context, arg models.GetLocationGeoParams) (models.GetLocationGeoRow, error)
	ListDueExternalStops(ctx context.Context, arg models.ListDueExternalStopsParams) ([]models.ListDueExternalStopsRow, error)
	SaveExternalStopResult(ctx context.Context, arg models.SaveExternalStopResultParams) (pgtype.Timestamptz, error)
	SaveExternalStopRetry(ctx context.Context, arg models.SaveExternalStopRetryParams) (int64, error)
	MarkExternalStopPushed(ctx context.Context, arg models.MarkExternalStopPushedParams) (int64, error)
	DeferExternalStopPush(ctx context.Context, arg models.DeferExternalStopPushParams) error
	WakeLocationExternalStops(ctx context.Context, arg models.WakeLocationExternalStopsParams) (int64, error)
	SetExternalStopPoint(ctx context.Context, arg models.SetExternalStopPointParams) (int64, error)
	SetExternalWorkCandidates(ctx context.Context, arg models.SetExternalWorkCandidatesParams) (int64, error)
	SetExternalWorkConfirmed(ctx context.Context, arg models.SetExternalWorkConfirmedParams) error
	DeleteExternalWork(ctx context.Context, orderID pgtype.UUID) error
	DeleteExternalWorkRead(ctx context.Context, arg models.DeleteExternalWorkReadParams) (int64, error)
	DeleteExternalWorkIfDone(ctx context.Context, orderID pgtype.UUID) error
	DeleteExternalStop(ctx context.Context, arg models.DeleteExternalStopParams) (int64, error)
	PruneExternalStops(ctx context.Context, now pgtype.Timestamptz) (int64, error)
	PruneExternalWork(ctx context.Context, now pgtype.Timestamptz) (int64, error)
	ListLiveExternalStops(ctx context.Context, arg models.ListLiveExternalStopsParams) ([]models.ListLiveExternalStopsRow, error)
	ListExternalStopsByIDs(ctx context.Context, arg models.ListExternalStopsByIDsParams) ([]models.ListExternalStopsByIDsRow, error)
	RequeueLocationExternalStops(ctx context.Context, arg models.RequeueLocationExternalStopsParams) (int64, error)
}

const (
	// openWindow bounds "open delivery" + the ingest safety sweep to the
	// current service (orders received in the last 12 h).
	openWindow = 12 * time.Hour
	// maxTableOthers caps the online deliveries in the drive-time table and
	// maxTableStops the external stops (OSRM's default table limit is 100
	// coordinates; the store is one more).
	maxTableOthers = 50
	maxTableStops  = 40
	// routeRetries: a routing failure is retried this many times before the
	// order is located without a route (a map pin beats no map).
	routeRetries = 3
	workers      = 6
	// StopTTL: an external stop (a point only) is dropped this long after the
	// agent asked to locate it, if geo.forget never came.
	StopTTL = 24 * time.Hour
)

// Service is the geo worker + the agent-facing geo RPCs + store geocoding.
type Service struct {
	q       Queries
	tx      func(ctx context.Context, fn func(q Queries) error) error
	tenancy Stores
	req     Requester
	logger  zerolog.Logger
	now     func() time.Time

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
	return newService(models.New(pool), func(ctx context.Context, fn func(q Queries) error) error {
		return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return fn(models.New(tx)) })
	}, tenancy, req, cfg, logger)
}

func newService(q Queries, tx func(context.Context, func(Queries) error) error, stores Stores, req Requester, cfg Config, logger zerolog.Logger) *Service {
	m := otel.Meter(observability.MeterName)
	s := &Service{
		q: q, tx: tx, tenancy: stores, req: req, base: cfg,
		logger:       logger.With().Str("module", "geo").Logger(),
		now:          time.Now,
		wake:         make(chan struct{}, 1),
		sem:          make(chan struct{}, workers),
		inflight:     map[uuid.UUID]bool{},
		PushTimeout:  5 * time.Second,
		ConnectDelay: 3 * time.Second,
	}
	s.located, _ = m.Int64Counter("geo.orders", metric.WithDescription("Deliveries worked by the geo worker (online orders + external stops), by result (located / check / not_found / retry)"))
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
	if _, err := s.q.InsertOrderGeo(ctx, models.InsertOrderGeoParams{
		OrderID: pgUUID(uuid.UUID(o.ID())), TenantID: pgUUID(uuid.UUID(o.TenantID())), LocationID: pgUUID(uuid.UUID(o.LocationID())),
	}); err != nil {
		s.logger.Warn().Ctx(ctx).Err(err).Str("order_id", uuid.UUID(o.ID()).String()).Msg("geo enqueue failed (sweep will retry)")
		return
	}
	s.poke()
}

// WakeLocation re-pushes everything a (re)connected agent has not acked —
// online orders and external stops (broker subscribe hook). Runs after
// ConnectDelay, off the caller's path.
func (s *Service) WakeLocation(scope sseBroker.Scope) {
	go func() {
		time.Sleep(s.ConnectDelay)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		n, err := s.q.WakeLocationOrderGeo(ctx, models.WakeLocationOrderGeoParams{
			TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID),
		})
		if err != nil {
			s.logger.Warn().Err(err).Msg("geo: wake location failed")
			return
		}
		m, err := s.q.WakeLocationExternalStops(ctx, models.WakeLocationExternalStopsParams{
			TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID),
		})
		if err != nil {
			s.logger.Warn().Err(err).Msg("geo: wake location (external stops) failed")
		}
		if n+m > 0 {
			s.logger.Info().Str("location_id", scope.LocationID.String()).Int64("rows", n).Int64("stops", m).Msg("geo: re-pushing unacked results to the reconnected agent")
			s.poke()
		}
	}()
}

// --- worker ------------------------------------------------------------------------

// Run works due order_geo rows and external stops until ctx ends.
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
		now := s.now()
		if now.Sub(lastSweep) > time.Minute {
			lastSweep = now
			s.sweep(ctx, now)
		}
		rows, err := s.q.ListDueOrderGeo(ctx, models.ListDueOrderGeoParams{Now: ts(now), MaxRows: 50})
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn().Err(err).Msg("geo: list due failed")
			}
			continue
		}
		stops, err := s.q.ListDueExternalStops(ctx, models.ListDueExternalStopsParams{Now: ts(now), MaxRows: 50})
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn().Err(err).Msg("geo: list due external stops failed")
			}
			stops = nil
		}
		jobs := make([]func(), 0, len(rows)+len(stops))
		ids := make([]uuid.UUID, 0, len(rows)+len(stops))
		for _, r := range rows {
			jobs = append(jobs, func() { s.process(ctx, r) })
			ids = append(ids, uuid.UUID(r.OrderID.Bytes))
		}
		for _, r := range stops {
			jobs = append(jobs, func() { s.processStop(ctx, r) })
			ids = append(ids, uuid.UUID(r.OrderID.Bytes))
		}
		for i, job := range jobs {
			id := ids[i]
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
			go func() {
				defer wg.Done()
				defer func() {
					<-s.sem
					s.mu.Lock()
					delete(s.inflight, id)
					s.mu.Unlock()
				}()
				job()
			}()
		}
	}
}

// sweep is the once-a-minute housekeeping: enqueue delivery orders the
// after-ingest hook missed, drop expired external stops and any address text
// past its 6 h limit.
func (s *Service) sweep(ctx context.Context, now time.Time) {
	if n, err := s.q.EnqueueMissingOrderGeo(ctx, ts(now.Add(-openWindow))); err != nil {
		if ctx.Err() == nil {
			s.logger.Warn().Err(err).Msg("geo: sweep failed")
		}
	} else if n > 0 {
		s.logger.Info().Int64("rows", n).Msg("geo: sweep enqueued missed delivery orders")
	}
	if n, err := s.q.PruneExternalStops(ctx, ts(now)); err != nil {
		if ctx.Err() == nil {
			s.logger.Warn().Err(err).Msg("geo: prune external stops failed")
		}
	} else if n > 0 {
		s.logger.Info().Int64("rows", n).Msg("geo: expired external stops dropped")
	}
	if n, err := s.q.PruneExternalWork(ctx, ts(now)); err != nil {
		if ctx.Err() == nil {
			s.logger.Warn().Err(err).Msg("geo: prune external addresses failed")
		}
	} else if n > 0 {
		s.logger.Info().Int64("rows", n).Msg("geo: external stop addresses discarded (6 h)")
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
				_ = s.q.DeferOrderGeoPush(ctx, models.DeferOrderGeoPushParams{OrderID: r.OrderID, NextTryAt: ts(s.now().Add(30 * time.Second))})
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
	candidates              []Candidate // state check only
}

// locateIn is what one locate works from.
type locateIn struct {
	key     string // geo_cache key; "" = none
	addr    Address
	hasAddr bool
	// fixed: a point already decided (an external stop's pin / choice, or a
	// re-route after the store moved) — no geocode.
	fixed        *Point
	fixedQuality string
	store        *Point
	radiusM      int
}

// resolve geocodes (cache first, then the providers with the confidence
// rule) and routes from the store. done=false means saveRetry scheduled a
// retry after a provider failure.
func (s *Service) resolve(ctx context.Context, in locateIn, attempts int, created time.Time,
	saveRetry func(next time.Time, cause error) error) (located, bool, error) {
	prov := s.Providers()
	res := located{state: StateNotFound}

	retry := func(cause error) (bool, error) {
		next, ok := NextTry(created, s.now(), attempts+1)
		if !ok {
			return true, nil // caller saves the final result
		}
		s.located.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "retry")))
		s.logger.Info().Ctx(ctx).Err(cause).Time("next_try_at", next).Msg("geo: provider failure, will retry")
		return false, saveRetry(next, cause)
	}

	switch {
	case in.fixed != nil:
		pt := *in.fixed
		res.pt, res.quality = &pt, in.fixedQuality
	default:
		var cached models.GeoCache
		err := pgx.ErrNoRows
		if in.key != "" {
			cached, err = s.q.GetGeoCache(ctx, in.key)
		}
		switch {
		case err == nil:
			pt := Point{Lat: cached.Lat, Lng: cached.Lng}
			res.pt, res.quality = &pt, cached.Quality
			if cached.Pinned {
				res.quality = QualityPinned
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return res, false, err
		case !in.hasAddr:
			res.lastErr = "no delivery address"
		default:
			g, gerr := prov.Geocode(ctx, in.addr, in.store)
			switch {
			case gerr == nil:
				cands := g.Candidates
				if len(cands) == 0 {
					cands = []Candidate{{Lat: g.Lat, Lng: g.Lng, Quality: g.Quality}}
				}
				if !Confident(in.addr, cands) {
					res.state = StateCheck
					res.candidates = cands[:min(len(cands), MaxCandidates)]
					return res, true, nil
				}
				res.pt, res.quality = &g.Point, g.Quality
				if err := s.q.UpsertGeoCacheGeocoded(ctx, models.UpsertGeoCacheGeocodedParams{
					AddressKey: in.key, Lat: g.Lat, Lng: g.Lng, Quality: g.Quality, Source: g.Source,
				}); err != nil {
					return res, false, err
				}
			case errors.Is(gerr, ErrNotFound):
				res.lastErr = "no geocoder match"
			default:
				if ctx.Err() != nil {
					return res, false, ctx.Err()
				}
				if done, err := retry(gerr); !done || err != nil {
					return res, done, err
				}
				res.lastErr = "gave up: " + gerr.Error()
			}
		}
	}

	if res.pt != nil {
		res.state = StateLocated
		if in.store != nil {
			store := *in.store
			res.outside = OutsideArea(store, *res.pt, in.radiusM)
			if prov.Routing() {
				rt, rerr := prov.Route(ctx, store, *res.pt)
				switch {
				case rerr == nil:
					res.route = rt
					d := rt.DurationS
					res.storeTo = &d
				case errors.Is(rerr, ErrNoRoute):
					res.lastErr = "no route from the store"
				default:
					if ctx.Err() != nil {
						return res, false, ctx.Err()
					}
					if attempts+1 < routeRetries {
						if done, err := retry(rerr); !done || err != nil {
							return res, done, err
						}
					}
					res.lastErr = "located without a route: " + rerr.Error()
				}
				if tbl, terr := prov.Table(ctx, []Point{*res.pt}, []Point{store}); terr == nil && len(tbl) == 1 && len(tbl[0]) == 1 && tbl[0][0] >= 0 {
					v := tbl[0][0]
					res.toStore = &v
				}
			}
		}
	}
	return res, true, nil
}

// locate works one pending order_geo row. done=false means a retry was scheduled.
func (s *Service) locate(ctx context.Context, scope sseBroker.Scope, id uuid.UUID, attempts int, created time.Time) (bool, error) {
	o, err := s.q.GetOrderForGeo(ctx, models.GetOrderForGeoParams{OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, s.q.DeferOrderGeoPush(ctx, models.DeferOrderGeoPushParams{OrderID: pgUUID(id), NextTryAt: ts(s.now().Add(time.Hour))})
	}
	if err != nil {
		return false, err
	}
	addr, hasAddr := orderAddress(o.FulfillmentAddress)
	in := locateIn{key: cacheKey(id, addr, hasAddr), hasAddr: hasAddr, radiusM: int(o.DeliveryRadiusM),
		addr: Address{Line1: addr.Line1, City: addr.City, State: addr.State, Zip: addr.PostalCode}}
	if o.StoreLat.Valid && o.StoreLng.Valid {
		in.store = &Point{Lat: o.StoreLat.Float64, Lng: o.StoreLng.Float64}
	}
	res, done, err := s.resolve(ctx, in, attempts, created, func(next time.Time, cause error) error {
		return s.q.SaveOrderGeoRetry(ctx, models.SaveOrderGeoRetryParams{OrderID: pgUUID(id), NextTryAt: ts(next), LastError: trunc(cause.Error())})
	})
	if err != nil || !done {
		return done, err
	}
	if err := s.saveResult(ctx, id, res); err != nil {
		return false, err
	}
	s.located.Add(ctx, 1, metric.WithAttributes(attribute.String("result", res.state)))
	s.logger.Info().Ctx(ctx).Str("order_id", id.String()).Str("state", res.state).Str("quality", res.quality).
		Bool("outside_area", res.outside).Int("candidates", len(res.candidates)).Str("note", res.lastErr).Msg("geo: order located")
	return true, nil
}

// candidatesJSON is the stored form of a check result's candidates (nil
// otherwise).
func candidatesJSON(cs []Candidate) []byte {
	if len(cs) == 0 {
		return nil
	}
	b, _ := json.Marshal(cs)
	return b
}

func (s *Service) saveResult(ctx context.Context, id uuid.UUID, r located) error {
	p := models.SaveOrderGeoResultParams{
		OrderID: pgUUID(id), State: r.state, Quality: r.quality, DistanceM: int32(r.route.DistanceM),
		DurationS: int32(r.route.DurationS), Polyline: r.route.Polyline, OutsideArea: r.outside, LastError: trunc(r.lastErr),
		Candidates: candidatesJSON(r.candidates),
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
	_, err := s.q.SaveOrderGeoResult(ctx, p)
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

// Push is the order.geo payload (contract/agent.md, main → agent). OrderID is
// main's order id for an online order, the agent's own id for an external
// stop (geo.locate).
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
	// State check (2.1.0): up to 3 matches for staff to pick from (geo.choose
	// by index). No lat/lng on the push itself.
	Candidates []Candidate `json:"candidates,omitempty"`
	// After a pick: the chosen candidate's address parts.
	ConfirmedAddress *ConfirmedAddress `json:"confirmed_address,omitempty"`
	// Confirmed: this push is the direct result of staff's geo.choose or
	// geo.pin (the agent emits address.confirmed only then). Absent on every
	// other push — a first locate, a cache hit on a pinned address, a re-route
	// after the store moved, a re-push after the acked one.
	Confirmed bool `json:"confirmed,omitempty"`
}

// geoRow is the push-relevant part of an order_geo row or an external stop.
type geoRow struct {
	state, quality, polyline string
	lat, lng                 pgtype.Float8
	distanceM, durationS     int32
	outside                  bool
	storeToS, toStoreS       pgtype.Int4
	confirmed                bool // the row's pending pin / choice (located only)
}

// buildPush fills the payload from a stored result (drive times included).
func (s *Service) buildPush(ctx context.Context, scope sseBroker.Scope, id uuid.UUID, g geoRow) Push {
	p := Push{OrderID: id.String(), State: g.state, DistanceM: int(g.distanceM), DurationS: int(g.durationS),
		Polyline: g.polyline, OutsideArea: g.outside}
	if g.state == StateLocated && g.lat.Valid && g.lng.Valid {
		lat, lng := g.lat.Float64, g.lng.Float64
		p.Lat, p.Lng, p.Quality = &lat, &lng, g.quality
		p.Confirmed = g.confirmed
		if g.storeToS.Valid {
			v := int(g.storeToS.Int32)
			p.StoreToS = &v
		}
		if g.toStoreS.Valid {
			v := int(g.toStoreS.Int32)
			p.ToStoreS = &v
		}
		s.addTimes(ctx, scope, id, Point{Lat: lat, Lng: lng}, &p)
	}
	return p
}

func decodeCandidates(raw []byte) []Candidate {
	if len(raw) == 0 {
		return nil
	}
	var cs []Candidate
	if json.Unmarshal(raw, &cs) != nil {
		return nil
	}
	return cs
}

func decodeConfirmed(raw []byte) *ConfirmedAddress {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var c ConfirmedAddress
	if json.Unmarshal(raw, &c) != nil {
		return nil
	}
	return &c
}

func (s *Service) push(ctx context.Context, scope sseBroker.Scope, id uuid.UUID) error {
	g, err := s.q.GetOrderGeo(ctx, pgUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if g.State == StatePending || g.PushedAt.Valid {
		return nil
	}
	p := s.buildPush(ctx, scope, id, geoRow{state: g.State, quality: g.Quality, polyline: g.Polyline, lat: g.Lat, lng: g.Lng,
		distanceM: g.DistanceM, durationS: g.DurationS, outside: g.OutsideArea, storeToS: g.StoreToS, toStoreS: g.ToStoreS,
		confirmed: g.Confirmed})
	switch g.State {
	case StateCheck:
		p.Candidates = decodeCandidates(g.Candidates)
	case StateLocated:
		p.ConfirmedAddress = decodeConfirmed(g.ConfirmedAddress)
	}
	return s.deliver(ctx, scope, p, g.CreatedAt.Time,
		func() error {
			_, err := s.q.MarkOrderGeoPushed(ctx, models.MarkOrderGeoPushedParams{OrderID: pgUUID(id), UpdatedAt: g.UpdatedAt})
			return err
		},
		func(d time.Duration) error {
			return s.q.DeferOrderGeoPush(ctx, models.DeferOrderGeoPushParams{OrderID: pgUUID(id), NextTryAt: ts(s.now().Add(d))})
		})
}

// deliver sends one order.geo and records the outcome: markPushed on an ack
// (or when the row is too old to keep trying), deferFor otherwise.
func (s *Service) deliver(ctx context.Context, scope sseBroker.Scope, p Push, created time.Time,
	markPushed func() error, deferFor func(time.Duration) error) error {
	stale := s.now().Sub(created) > openWindow
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

// addTimes fills the drive times between this delivery, the store and every
// other open located delivery of the location — online orders and external
// stops alike, so the Pi's AUTO has one matrix (best effort: a routing
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
	online, err := s.q.ListOpenLocatedDeliveries(ctx, models.ListOpenLocatedDeliveriesParams{
		TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID), ExcludeID: pgUUID(id),
		Since: ts(s.now().Add(-openWindow)), MaxRows: maxTableOthers,
	})
	if err != nil {
		s.logger.Warn().Ctx(ctx).Err(err).Msg("geo: list open deliveries failed")
		return
	}
	stops, err := s.q.ListLiveExternalStops(ctx, models.ListLiveExternalStopsParams{
		TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID), ExcludeID: pgUUID(id), MaxRows: maxTableStops,
	})
	if err != nil {
		s.logger.Warn().Ctx(ctx).Err(err).Msg("geo: list external stops failed")
		return
	}
	type other struct {
		id string
		pt Point
	}
	others := make([]other, 0, len(online)+len(stops))
	for _, o := range online {
		others = append(others, other{uuid.UUID(o.OrderID.Bytes).String(), Point{Lat: o.Lat.Float64, Lng: o.Lng.Float64}})
	}
	for _, o := range stops {
		others = append(others, other{uuid.UUID(o.OrderID.Bytes).String(), Point{Lat: o.Lat.Float64, Lng: o.Lng.Float64}})
	}
	pts := []Point{{Lat: *loc.Lat, Lng: *loc.Lng}}
	for _, o := range others {
		pts = append(pts, o.pt)
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
		p.Times = append(p.Times, TimePair{Other: o.id, ToS: to, FromS: from})
	}
}

// --- agent → main RPCs ------------------------------------------------------------

// HandlePin is the rpc.InboundHandler for geo.pin {order_id, lat, lng}: staff
// dropped a pin for an order on the tablet. Saved pinned in geo_cache for the
// order's normalized address (so the next order there lands right), then the
// order is re-worked and order.geo pushed again with quality pinned. An
// external stop (no orders row, 2.1.0) takes the pin as its point.
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
	q := s.q
	o, err := q.GetOrderForGeo(ctx, models.GetOrderForGeoParams{OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return s.pinStop(ctx, scope, id, Point{Lat: *b.Lat, Lng: *b.Lng})
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
	rows, err := s.q.ListOrderGeoByIDs(ctx, models.ListOrderGeoByIDsParams{
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
	// External stops (POS deliveries, 2.1.0) can share a run.
	extStops, err := s.q.ListExternalStopsByIDs(ctx, models.ListExternalStopsByIDsParams{
		TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID), Ids: ids,
	})
	if err != nil {
		return nil, err
	}
	for _, r := range extStops {
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
// are cached (an external stop keeps its own point), so this is routing only.
func (s *Service) StoreMoved(ctx context.Context, tenantID, locationID uuid.UUID) {
	if s.storeChanged != nil {
		s.storeChanged(ctx, tenantID, locationID)
	}
	n, err := s.q.RequeueLocationOrderGeo(ctx, models.RequeueLocationOrderGeoParams{
		TenantID: pgUUID(tenantID), LocationID: pgUUID(locationID), Since: ts(s.now().Add(-openWindow)),
	})
	if err != nil {
		s.logger.Warn().Ctx(ctx).Err(err).Msg("geo: requeue open deliveries failed")
		return
	}
	m, err := s.q.RequeueLocationExternalStops(ctx, models.RequeueLocationExternalStopsParams{
		TenantID: pgUUID(tenantID), LocationID: pgUUID(locationID),
	})
	if err != nil {
		s.logger.Warn().Ctx(ctx).Err(err).Msg("geo: requeue external stops failed")
	}
	if n+m > 0 {
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
