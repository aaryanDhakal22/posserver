package geo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"quiccpos/main/internal/infra/database/models"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// External stops (2.1.0): a delivery main has no order for — a POS check the
// store hub pulled into the agent. The agent asks with RPC geo.locate {order_id
// (its own id), address}; main answers {ok} at once, then geocodes + routes it
// like an online delivery and pushes order.geo with that order_id. The stop
// keeps a point only (geo_external_stops, 24 h); the address text sits in
// geo_external_work only while it is needed (≤ 6 h). Located stops join every
// drive-time table of the location, so AUTO plans online + POS together.

// stopAddress is the geo.locate address (and geo_external_work.address).
type stopAddress struct {
	Line1      string `json:"line1"`
	Line2      string `json:"line2"`
	City       string `json:"city"`
	State      string `json:"state"`
	PostalCode string `json:"postal_code"`
}

func (a stopAddress) geo() Address {
	return Address{Line1: strings.TrimSpace(a.Line1), City: strings.TrimSpace(a.City), State: strings.TrimSpace(a.State), Zip: strings.TrimSpace(a.PostalCode)}
}

// AddressDiscardAfter bounds how long an external stop's address text is kept.
const AddressDiscardAfter = GiveUpAfter

// HandleLocate is the rpc.InboundHandler for geo.locate {order_id, address}.
func (s *Service) HandleLocate(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b struct {
		OrderID string       `json:"order_id"`
		Address *stopAddress `json:"address"`
	}
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("%w: geo.locate: %v", rpc.ErrBadPayload, err)
	}
	id, err := uuid.Parse(b.OrderID)
	if err != nil || b.Address == nil {
		return nil, fmt.Errorf("%w: geo.locate needs {order_id, address}", rpc.ErrBadPayload)
	}
	addr := *b.Address
	key := ""
	if a := addr.geo(); a.Line1 != "" {
		key = NormalizeKey(a)
	}
	raw, _ := json.Marshal(addr)
	now := s.now()
	conflict := false
	err = s.tx(ctx, func(q Queries) error {
		n, err := q.UpsertExternalStop(ctx, models.UpsertExternalStopParams{
			OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID), ExpiresAt: ts(now.Add(StopTTL)),
		})
		if err != nil {
			return err
		}
		if n == 0 { // the id belongs to another location
			conflict = true
			return nil
		}
		return q.UpsertExternalWork(ctx, models.UpsertExternalWorkParams{
			OrderID: pgUUID(id), Address: raw, AddressKey: key, DiscardAt: ts(now.Add(AddressDiscardAfter)),
		})
	})
	if err != nil {
		return nil, err
	}
	if conflict {
		return map[string]any{"ok": false, "code": "conflict"}, nil
	}
	s.logger.Info().Ctx(ctx).Str("order_id", id.String()).Msg("geo: external stop queued")
	s.poke()
	return map[string]any{"ok": true}, nil
}

// HandleForget is the rpc.InboundHandler for geo.forget {order_id}: the POS
// order is gone (void, batch close) — drop its stop and any address text.
func (s *Service) HandleForget(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b struct {
		OrderID string `json:"order_id"`
	}
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("%w: geo.forget: %v", rpc.ErrBadPayload, err)
	}
	id, err := uuid.Parse(b.OrderID)
	if err != nil {
		return nil, fmt.Errorf("%w: geo.forget needs {order_id}", rpc.ErrBadPayload)
	}
	n, err := s.q.DeleteExternalStop(ctx, models.DeleteExternalStopParams{
		OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID),
	})
	if err != nil {
		return nil, err
	}
	if n > 0 {
		s.logger.Info().Ctx(ctx).Str("order_id", id.String()).Msg("geo: external stop forgotten")
	}
	return map[string]any{"ok": true}, nil
}

// HandleChoose is the rpc.InboundHandler for geo.choose {order_id, index}:
// staff picked one of the candidates of a check result. The pick is saved
// pinned in geo_cache for the input address, then the delivery is routed and
// pushed again as located / pinned with confirmed_address. Works for online
// orders and external stops.
func (s *Service) HandleChoose(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b struct {
		OrderID string `json:"order_id"`
		Index   *int   `json:"index"`
	}
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("%w: geo.choose: %v", rpc.ErrBadPayload, err)
	}
	id, err := uuid.Parse(b.OrderID)
	if err != nil || b.Index == nil || *b.Index < 0 {
		return nil, fmt.Errorf("%w: geo.choose needs {order_id, index >= 0}", rpc.ErrBadPayload)
	}
	idx := *b.Index
	fail := func(code string) (any, error) { return map[string]any{"ok": false, "code": code}, nil }

	o, err := s.q.GetOrderForGeo(ctx, models.GetOrderForGeoParams{OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID)})
	switch {
	case err == nil: // an online order
		g, err := s.q.GetOrderGeo(ctx, pgUUID(id))
		if errors.Is(err, pgx.ErrNoRows) {
			return fail("no_candidates")
		}
		if err != nil {
			return nil, err
		}
		cands := decodeCandidates(g.Candidates)
		if g.State != StateCheck || len(cands) == 0 {
			return fail("no_candidates")
		}
		if idx >= len(cands) {
			return fail("bad_index")
		}
		c := cands[idx]
		addr, ok := orderAddress(o.FulfillmentAddress)
		if err := s.q.UpsertGeoCachePinned(ctx, models.UpsertGeoCachePinnedParams{AddressKey: cacheKey(id, addr, ok), Lat: c.Lat, Lng: c.Lng}); err != nil {
			return nil, err
		}
		conf, _ := json.Marshal(c.Confirmed())
		if err := s.q.ResetOrderGeoForChoice(ctx, models.ResetOrderGeoForChoiceParams{OrderID: pgUUID(id), ConfirmedAddress: conf}); err != nil {
			return nil, err
		}
	case errors.Is(err, pgx.ErrNoRows): // an external stop?
		if _, err := s.q.GetExternalStop(ctx, models.GetExternalStopParams{OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID)}); errors.Is(err, pgx.ErrNoRows) {
			return fail("not_found")
		} else if err != nil {
			return nil, err
		}
		w, err := s.q.GetExternalWork(ctx, pgUUID(id))
		if errors.Is(err, pgx.ErrNoRows) {
			return fail("no_candidates")
		}
		if err != nil {
			return nil, err
		}
		cands := decodeCandidates(w.Candidates)
		if len(cands) == 0 {
			return fail("no_candidates")
		}
		if idx >= len(cands) {
			return fail("bad_index")
		}
		c := cands[idx]
		if w.AddressKey != "" {
			if err := s.q.UpsertGeoCachePinned(ctx, models.UpsertGeoCachePinnedParams{AddressKey: w.AddressKey, Lat: c.Lat, Lng: c.Lng}); err != nil {
				return nil, err
			}
		}
		conf, _ := json.Marshal(c.Confirmed())
		if err := s.q.SetExternalWorkConfirmed(ctx, models.SetExternalWorkConfirmedParams{OrderID: pgUUID(id), ConfirmedAddress: conf}); err != nil {
			return nil, err
		}
		if _, err := s.q.SetExternalStopPoint(ctx, models.SetExternalStopPointParams{
			OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID),
			Lat: pgtype.Float8{Float64: c.Lat, Valid: true}, Lng: pgtype.Float8{Float64: c.Lng, Valid: true}, Quality: QualityPinned,
		}); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	s.logger.Info().Ctx(ctx).Str("order_id", id.String()).Int("index", idx).Msg("geo: candidate chosen")
	s.poke()
	return map[string]any{"ok": true}, nil
}

// pinStop is geo.pin for an external stop: the pin becomes its point (and, if
// main still holds the address, the pinned geo_cache entry for it).
func (s *Service) pinStop(ctx context.Context, scope sseBroker.Scope, id uuid.UUID, p Point) (any, error) {
	if _, err := s.q.GetExternalStop(ctx, models.GetExternalStopParams{OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID)}); errors.Is(err, pgx.ErrNoRows) {
		return map[string]any{"ok": false, "code": "not_found"}, nil
	} else if err != nil {
		return nil, err
	}
	w, err := s.q.GetExternalWork(ctx, pgUUID(id))
	switch {
	case err == nil:
		if w.AddressKey != "" {
			if err := s.q.UpsertGeoCachePinned(ctx, models.UpsertGeoCachePinnedParams{AddressKey: w.AddressKey, Lat: p.Lat, Lng: p.Lng}); err != nil {
				return nil, err
			}
		}
		// A pin has no address: nothing left to keep.
		if err := s.q.DeleteExternalWork(ctx, pgUUID(id)); err != nil {
			return nil, err
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}
	if _, err := s.q.SetExternalStopPoint(ctx, models.SetExternalStopPointParams{
		OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID),
		Lat: pgtype.Float8{Float64: p.Lat, Valid: true}, Lng: pgtype.Float8{Float64: p.Lng, Valid: true}, Quality: QualityPinned,
	}); err != nil {
		return nil, err
	}
	s.logger.Info().Ctx(ctx).Str("order_id", id.String()).Msg("geo: pin saved for an external stop")
	s.poke()
	return map[string]any{"ok": true}, nil
}

// --- worker ----------------------------------------------------------------------

func (s *Service) processStop(ctx context.Context, r models.ListDueExternalStopsRow) {
	id := uuid.UUID(r.OrderID.Bytes)
	scope := sseBroker.Scope{TenantID: uuid.UUID(r.TenantID.Bytes), LocationID: uuid.UUID(r.LocationID.Bytes)}
	log := s.logger.With().Str("order_id", id.String()).Str("kind", "external").Logger()
	if r.State == StatePending {
		done, err := s.locateStop(ctx, scope, id)
		if err != nil {
			if ctx.Err() == nil {
				log.Warn().Err(err).Msg("geo: locate external stop failed (db)")
				_ = s.q.DeferExternalStopPush(ctx, models.DeferExternalStopPushParams{OrderID: r.OrderID, NextTryAt: ts(s.now().Add(30 * time.Second))})
			}
			return
		}
		if !done {
			return
		}
	}
	if err := s.pushStop(ctx, scope, id); err != nil && ctx.Err() == nil {
		log.Warn().Err(err).Msg("geo: push external stop failed (db)")
	}
}

// locateStop works one pending external stop: its own point when it has one
// (pin, choice, store moved), else the address in geo_external_work.
// done=false: a retry was scheduled, or the stop changed meanwhile.
func (s *Service) locateStop(ctx context.Context, scope sseBroker.Scope, id uuid.UUID) (bool, error) {
	st, err := s.q.GetExternalStop(ctx, models.GetExternalStopParams{OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // forgotten meanwhile
	}
	if err != nil {
		return false, err
	}
	var work *models.GeoExternalWork
	if w, err := s.q.GetExternalWork(ctx, pgUUID(id)); err == nil {
		work = &w
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	in := locateIn{}
	created := st.CreatedAt.Time
	if l, err := s.q.GetLocationGeo(ctx, models.GetLocationGeoParams{LocationID: pgUUID(scope.LocationID), TenantID: pgUUID(scope.TenantID)}); err == nil {
		in.radiusM = int(l.DeliveryRadiusM)
		if l.Lat.Valid && l.Lng.Valid {
			in.store = &Point{Lat: l.Lat.Float64, Lng: l.Lng.Float64}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	switch {
	case st.Lat.Valid && st.Lng.Valid:
		in.fixed, in.fixedQuality = &Point{Lat: st.Lat.Float64, Lng: st.Lng.Float64}, st.Quality
	case work != nil:
		var a stopAddress
		_ = json.Unmarshal(work.Address, &a)
		in.addr = a.geo()
		in.hasAddr = in.addr.Line1 != ""
		in.key = work.AddressKey
		created = work.CreatedAt.Time
	}
	res, done, err := s.resolve(ctx, in, int(st.Attempts), created, func(next time.Time, cause error) error {
		// 0 rows: re-located / pinned meanwhile, which made it due again.
		_, err := s.q.SaveExternalStopRetry(ctx, models.SaveExternalStopRetryParams{OrderID: pgUUID(id), NextTryAt: ts(next), LastError: trunc(cause.Error()), ReadAt: st.UpdatedAt})
		return err
	})
	if err != nil || !done {
		return false, err
	}
	if work == nil && !in.hasAddr && in.fixed == nil {
		res.lastErr = "address discarded"
	}
	p := models.SaveExternalStopResultParams{
		OrderID: pgUUID(id), ReadAt: st.UpdatedAt, State: res.state, Quality: res.quality,
		DistanceM: int32(res.route.DistanceM), DurationS: int32(res.route.DurationS), Polyline: res.route.Polyline,
		OutsideArea: res.outside, LastError: trunc(res.lastErr),
	}
	if res.pt != nil {
		p.Lat, p.Lng = pgtype.Float8{Float64: res.pt.Lat, Valid: true}, pgtype.Float8{Float64: res.pt.Lng, Valid: true}
	}
	if res.storeTo != nil {
		p.StoreToS = pgtype.Int4{Int32: int32(*res.storeTo), Valid: true}
	}
	if res.toStore != nil {
		p.ToStoreS = pgtype.Int4{Int32: int32(*res.toStore), Valid: true}
	}
	// The result and its work-row follow-up commit together, and the
	// follow-up only touches the work row this run read (created_at): a
	// geo.locate racing in with a new address keeps its row, and its stop
	// reset makes the result save miss (due again).
	saved := false
	if err := s.tx(ctx, func(q Queries) error {
		if _, err := q.SaveExternalStopResult(ctx, p); errors.Is(err, pgx.ErrNoRows) {
			return nil // re-located / pinned / forgotten meanwhile: due again
		} else if err != nil {
			return err
		}
		saved = true
		if work == nil {
			return nil
		}
		switch {
		case res.state == StateCheck:
			_, err := q.SetExternalWorkCandidates(ctx, models.SetExternalWorkCandidatesParams{
				OrderID: pgUUID(id), Candidates: candidatesJSON(res.candidates), ReadAt: work.CreatedAt})
			return err
		case decodeConfirmed(work.ConfirmedAddress) == nil:
			// Located or given up: the address text is not needed any more.
			_, err := q.DeleteExternalWorkRead(ctx, models.DeleteExternalWorkReadParams{OrderID: pgUUID(id), ReadAt: work.CreatedAt})
			return err
		}
		return nil
	}); err != nil {
		return false, err
	}
	if !saved {
		return false, nil
	}
	s.located.Add(ctx, 1, metric.WithAttributes(attribute.String("result", res.state), attribute.String("kind", "external")))
	s.logger.Info().Ctx(ctx).Str("order_id", id.String()).Str("state", res.state).Str("quality", res.quality).
		Bool("outside_area", res.outside).Int("candidates", len(res.candidates)).Str("note", res.lastErr).Msg("geo: external stop located")
	return true, nil
}

func (s *Service) pushStop(ctx context.Context, scope sseBroker.Scope, id uuid.UUID) error {
	st, err := s.q.GetExternalStop(ctx, models.GetExternalStopParams{OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.State == StatePending || st.PushedAt.Valid {
		return nil
	}
	p := s.buildPush(ctx, scope, id, geoRow{state: st.State, quality: st.Quality, polyline: st.Polyline, lat: st.Lat, lng: st.Lng,
		distanceM: st.DistanceM, durationS: st.DurationS, outside: st.OutsideArea, storeToS: st.StoreToS, toStoreS: st.ToStoreS,
		confirmed: st.Confirmed})
	if st.State == StateCheck || st.State == StateLocated {
		if w, err := s.q.GetExternalWork(ctx, pgUUID(id)); err == nil {
			if st.State == StateCheck {
				p.Candidates = decodeCandidates(w.Candidates)
			} else {
				p.ConfirmedAddress = decodeConfirmed(w.ConfirmedAddress)
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	return s.deliver(ctx, scope, p, st.CreatedAt.Time,
		func() error {
			n, err := s.q.MarkExternalStopPushed(ctx, models.MarkExternalStopPushedParams{OrderID: pgUUID(id), UpdatedAt: st.UpdatedAt})
			if err != nil || n == 0 {
				return err
			}
			// Acked: a confirmed address has reached the agent.
			return s.q.DeleteExternalWorkIfDone(ctx, pgUUID(id))
		},
		func(d time.Duration) error {
			return s.q.DeferExternalStopPush(ctx, models.DeferExternalStopPushParams{OrderID: pgUUID(id), NextTryAt: ts(s.now().Add(d))})
		})
}
