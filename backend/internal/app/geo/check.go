package geo

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"quiccpos/main/internal/infra/database/models"
)

// Address check results (main 3.0 delivery check).
const (
	CheckInside   = "inside"  // located, within the delivery radius
	CheckOutside  = "outside" // located, farther than the radius
	CheckAmbig    = "check"   // several places match: ask which one
	CheckNotFound = "not_found"
	CheckNoStore  = "no_store_location" // the store has no pin yet
)

// AddressCheck is whether a store delivers to an address (straight line from
// the store pin vs its delivery radius, the same rule as the tablet's
// "outside area" tag).
type AddressCheck struct {
	Result     string
	DistanceM  int
	RadiusM    int
	Point      *Point
	Candidates []Candidate
}

// CheckAddress geocodes an address for a store (cache first, then the
// providers with the same confidence rule as orders; a confident answer is
// cached) and tells whether it is inside the delivery radius. It never
// retries: a provider failure is returned (the caller answers "try again").
// The address is never logged.
func (s *Service) CheckAddress(ctx context.Context, tenantID, locationID uuid.UUID, a Address) (AddressCheck, error) {
	l, err := s.tenancy.GetLocation(ctx, locationID)
	if err != nil {
		return AddressCheck{}, err
	}
	out := AddressCheck{RadiusM: l.DeliveryRadiusM}
	if l.Lat == nil || l.Lng == nil {
		out.Result = CheckNoStore
		return out, nil
	}
	store := Point{Lat: *l.Lat, Lng: *l.Lng}
	in := locateIn{tenantID: tenantID, locationID: locationID, key: NormalizeKey(a), addr: a, hasAddr: true,
		store: &store, radiusM: l.DeliveryRadiusM}

	var pt *Point
	c, _, err := s.cached(ctx, in)
	switch {
	case err == nil:
		pt = &Point{Lat: c.Lat, Lng: c.Lng}
	case !errors.Is(err, pgx.ErrNoRows):
		return out, err
	default:
		g, gerr := s.geocode(ctx, s.Providers(), in)
		switch {
		case errors.Is(gerr, ErrNotFound):
			out.Result = CheckNotFound
			return out, nil
		case gerr != nil:
			return out, gerr
		case len(g.check) > 0:
			out.Result, out.Candidates = CheckAmbig, g.check
			return out, nil
		}
		p := g.res.Point
		pt = &p
		if g.cacheKey != "" {
			if err := s.q.UpsertGeoCacheGeocoded(ctx, models.UpsertGeoCacheGeocodedParams{
				AddressKey: g.cacheKey, Lat: p.Lat, Lng: p.Lng, Quality: g.res.Quality, Source: g.res.Source,
			}); err != nil {
				s.logger.Warn().Ctx(ctx).Err(err).Msg("geo: cache an address check")
			}
		}
	}
	out.Point = pt
	out.DistanceM = int(HaversineM(store, *pt))
	out.Result = CheckInside
	if OutsideArea(store, *pt, l.DeliveryRadiusM) {
		out.Result = CheckOutside
	}
	return out, nil
}
