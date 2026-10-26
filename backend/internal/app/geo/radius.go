package geo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"quiccpos/main/internal/infra/database/models"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"
)

// Delivery radius from the Pi (2.2.0): the tablet's delivery settings set the
// location's radius through the agent (RPC location.radius). The radius is
// the same locations.delivery_radius_m the store page shows (its map circle).

// Radius limits (location.radius): 500 m to 50 miles.
const (
	MinRadiusM = 500
	MaxRadiusM = 80467
)

// RadiusResult is what a radius change touched.
type RadiusResult struct {
	RadiusM int `json:"radius_m"`
	// Reflagged counts the open deliveries (online orders + external stops)
	// whose outside_area flipped; each is pushed again as order.geo.
	Reflagged int `json:"-"`
}

// HandleRadius is the rpc.InboundHandler for location.radius {radius_m}:
// validates it, saves it on the calling agent's location, re-flags
// outside_area on that location's open located deliveries (each one whose
// flag changed is pushed again as order.geo by the worker) and pushes
// store_config. Reply {radius_m}. A bad radius is a 400 (ErrBadPayload).
func (s *Service) HandleRadius(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b struct {
		RadiusM *json.Number `json:"radius_m"`
	}
	if err := json.Unmarshal(payload, &b); err != nil || b.RadiusM == nil {
		return nil, fmt.Errorf("%w: location.radius needs {radius_m}", rpc.ErrBadPayload)
	}
	r, err := b.RadiusM.Int64()
	if err != nil || r < MinRadiusM || r > MaxRadiusM {
		return nil, fmt.Errorf("%w: radius_m must be a whole number of meters, %d-%d", rpc.ErrBadPayload, MinRadiusM, MaxRadiusM)
	}
	res, err := s.SetRadius(ctx, scope, int(r))
	if err != nil {
		return nil, err
	}
	return res, nil
}

// ErrNoLocation: the scope's location doesn't exist (any more).
var ErrNoLocation = errors.New("location not found")

// SetRadius saves the radius and re-flags the open deliveries (see
// HandleRadius).
func (s *Service) SetRadius(ctx context.Context, scope sseBroker.Scope, radiusM int) (*RadiusResult, error) {
	tid, lid := pgUUID(scope.TenantID), pgUUID(scope.LocationID)
	res := &RadiusResult{RadiusM: radiusM}
	err := s.tx(ctx, func(q Queries) error {
		n, err := q.SetLocationDeliveryRadius(ctx, models.SetLocationDeliveryRadiusParams{
			RadiusM: int32(radiusM), LocationID: lid, TenantID: tid})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNoLocation
		}
		store, err := q.GetLocationGeo(ctx, models.GetLocationGeoParams{LocationID: lid, TenantID: tid})
		if err != nil {
			return err
		}
		if !store.Lat.Valid || !store.Lng.Valid {
			return nil // no store pin: nothing is located from it
		}
		st := Point{Lat: store.Lat.Float64, Lng: store.Lng.Float64}
		rows, err := q.ListOpenLocatedOrderGeoForRadius(ctx, models.ListOpenLocatedOrderGeoForRadiusParams{
			TenantID: tid, LocationID: lid, Since: ts(s.now().Add(-openWindow))})
		if err != nil {
			return err
		}
		for _, g := range rows {
			out := OutsideArea(st, Point{Lat: g.Lat.Float64, Lng: g.Lng.Float64}, radiusM)
			if out == g.OutsideArea {
				continue
			}
			n, err := q.SetOrderGeoOutsideArea(ctx, models.SetOrderGeoOutsideAreaParams{OutsideArea: out, OrderID: g.OrderID})
			if err != nil {
				return err
			}
			res.Reflagged += int(n)
		}
		stops, err := q.ListLiveExternalStopsForRadius(ctx, models.ListLiveExternalStopsForRadiusParams{TenantID: tid, LocationID: lid})
		if err != nil {
			return err
		}
		for _, g := range stops {
			out := OutsideArea(st, Point{Lat: g.Lat.Float64, Lng: g.Lng.Float64}, radiusM)
			if out == g.OutsideArea {
				continue
			}
			n, err := q.SetExternalStopOutsideArea(ctx, models.SetExternalStopOutsideAreaParams{
				OutsideArea: out, OrderID: g.OrderID, TenantID: tid, LocationID: lid})
			if err != nil {
				return err
			}
			res.Reflagged += int(n)
		}
		return nil
	})
	if errors.Is(err, ErrNoLocation) {
		return nil, fmt.Errorf("%w: %v", rpc.ErrBadPayload, err)
	}
	if err != nil {
		return nil, err
	}
	s.logger.Info().Ctx(ctx).Str("location_id", scope.LocationID.String()).Int("radius_m", radiusM).
		Int("reflagged", res.Reflagged).Msg("geo: delivery radius set from the Pi")
	if s.storeChanged != nil {
		s.storeChanged(ctx, scope.TenantID, scope.LocationID)
	}
	if res.Reflagged > 0 {
		s.poke() // the worker re-pushes the re-flagged rows as order.geo
	}
	return res, nil
}
