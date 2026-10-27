package geo

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strings"

	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The area retry (2.2.2). The old POS (SoftTouch) sends its terminal's default
// town + zip with every delivery address ("1613 Wadsworth Way" arrives as
// Towson 21286 while it is Baltimore 21239) and cashiers type notes after the
// street ("5415 YORK RD, AROMA PIZZA"); online orders can carry the same
// mistakes. When the normal geocode finds nothing:
//
//  1. the street line is cleaned (CleanStreet: the first comma segment that
//     starts with a house number) and geocoded again with its city/state/zip;
//  2. still nothing: the cleaned street + state only (no town, no zip) is
//     searched inside the store's area (StoreArea) — Nominatim with a bounded
//     viewbox, the Census without town/zip and filtered to the box. Anything
//     outside the box is discarded.
//
// One match in the box locates the delivery with corrected=true; several are
// a check (staff pick). The corrected point is cached for the original input
// under a key scoped to the location (AreaCacheKey), since another store's
// box would have answered differently. Staff searches (geo.search) use the
// same box.

// BBox is a lng/lat box, stored like locations.map_bbox: [minLng, minLat,
// maxLng, maxLat].
type BBox struct {
	MinLng, MinLat, MaxLng, MaxLat float64
}

// Contains reports whether p lies inside the box (edges included).
func (b BBox) Contains(p Point) bool {
	return p.Lat >= b.MinLat && p.Lat <= b.MaxLat && p.Lng >= b.MinLng && p.Lng <= b.MaxLng
}

// Center is the box's middle.
func (b BBox) Center() Point {
	return Point{Lat: (b.MinLat + b.MaxLat) / 2, Lng: (b.MinLng + b.MaxLng) / 2}
}

// BBoxFrom reads a stored [minLng, minLat, maxLng, maxLat] (ok=false when it
// is missing or not a sane box).
func BBoxFrom(v []float64) (BBox, bool) {
	if len(v) != 4 {
		return BBox{}, false
	}
	b := BBox{MinLng: v[0], MinLat: v[1], MaxLng: v[2], MaxLat: v[3]}
	for _, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return BBox{}, false
		}
	}
	if b.MinLng >= b.MaxLng || b.MinLat >= b.MaxLat || b.MinLat < -90 || b.MaxLat > 90 || b.MinLng < -180 || b.MaxLng > 180 {
		return BBox{}, false
	}
	return b, true
}

// AreaHalfSideM is the half side of the box around the store pin used when
// the location has no map: 10 mi, the same square the console's map-build
// cuts (console maps.py bbox_around).
const AreaHalfSideM = 10 * 1609.344

// BoxAround is the square of half side halfM meters around p.
func BoxAround(p Point, halfM float64) BBox {
	dLat := halfM / 111195.0
	dLng := dLat / math.Cos(p.Lat*math.Pi/180)
	return BBox{MinLng: p.Lng - dLng, MinLat: p.Lat - dLat, MaxLng: p.Lng + dLng, MaxLat: p.Lat + dLat}
}

// StoreArea is the box deliveries are searched in: the location's published
// map bbox, else AreaHalfSideM around the store pin; ok=false with neither.
func StoreArea(mapBBox []float64, store *Point) (BBox, bool) {
	if b, ok := BBoxFrom(mapBBox); ok {
		return b, true
	}
	if store != nil {
		return BoxAround(*store, AreaHalfSideM), true
	}
	return BBox{}, false
}

// AreaCacheKey is the geo_cache key of an area-retry result: the input's own
// key scoped to the location whose box found it.
func AreaCacheKey(locationID uuid.UUID, key string) string {
	return "area|" + locationID.String() + "|" + key
}

var startsWithNumber = regexp.MustCompile(`^\s*\d`)

// CleanStreet keeps only the street part of a street line: the first comma
// segment that starts with a house number ("1613 Wadsworth Way, HOUSE" →
// "1613 Wadsworth Way"), unit dropped (stripUnit). A line with no such
// segment comes back unit-stripped only.
func CleanStreet(line1 string) string {
	for _, seg := range strings.Split(line1, ",") {
		if startsWithNumber.MatchString(seg) {
			return stripUnit(strings.Join(strings.Fields(seg), " "))
		}
	}
	return stripUnit(line1)
}

// AreaGeocoder searches inside a box only. The Address carries no town or zip
// (the area retry and staff searches drop them); the results are all inside
// box, best first, or ErrNotFound.
type AreaGeocoder interface {
	GeocodeIn(ctx context.Context, a Address, box BBox) (GeocodeResult, error)
}

// inBox keeps the candidates inside box (ErrNotFound when none is).
func inBox(r GeocodeResult, box BBox) (GeocodeResult, error) {
	cs := r.Candidates
	if len(cs) == 0 {
		cs = []Candidate{{Lat: r.Lat, Lng: r.Lng, Quality: r.Quality}}
	}
	keep := make([]Candidate, 0, len(cs))
	for _, c := range cs {
		if box.Contains(c.Point()) {
			keep = append(keep, c)
		}
	}
	return resultFrom(keep, r.Source)
}

// geocodeIn asks g for a inside box: through its own GeocodeIn when it has
// one, else a biased Geocode filtered to the box. The result is filtered
// either way — a provider that ignores the bound can't leak a far match.
func geocodeIn(ctx context.Context, g Geocoder, a Address, box BBox) (GeocodeResult, error) {
	var r GeocodeResult
	var err error
	if ag, ok := g.(AreaGeocoder); ok {
		r, err = ag.GeocodeIn(ctx, a, box)
	} else {
		c := box.Center()
		r, err = g.Geocode(ctx, a, &c)
	}
	if err != nil {
		return GeocodeResult{}, err
	}
	return inBox(r, box)
}

// MaxSearchResults caps a staff search's candidates (geo.search).
const MaxSearchResults = 5

// searchIn asks every geocoder of g (each member of a Chain; else g alone)
// for a inside box and merges their matches, best provider first, repeats
// dropped. ErrNotFound when none matched; a provider failure only when no
// geocoder matched.
func searchIn(ctx context.Context, g Geocoder, a Address, box BBox) ([]Candidate, error) {
	members := []Geocoder{g}
	if ch, ok := g.(Chain); ok {
		members = ch
	}
	var all []Candidate
	var failure error
	for _, m := range members {
		r, err := geocodeIn(ctx, m, a, box)
		switch {
		case err == nil:
			all = append(all, r.Candidates...)
		case errors.Is(err, ErrNotFound):
		default:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			failure = errors.Join(failure, err)
		}
	}
	if len(all) == 0 {
		if failure != nil {
			return nil, failure
		}
		return nil, ErrNotFound
	}
	all = mergeCandidates(all)
	return all[:min(len(all), MaxSearchResults)], nil
}

// mergeCandidates drops repeats across providers: the same point, the same
// label, or the same house (sameAddress) as one already kept.
func mergeCandidates(cs []Candidate) []Candidate {
	cs = dedupe(cs)
	out := make([]Candidate, 0, len(cs))
	for _, c := range cs {
		dup := sameLabel(out, c)
		for _, k := range out {
			if dup || sameAddress(k, c) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, c)
		}
	}
	return out
}

// areaPick decides an area-retry answer: the candidates that agree with the
// input's house number + street name win over the rest (a road-only match
// beside the house is not a second place), then one place is located and
// several are a check.
func areaPick(in Address, cs []Candidate) (one *Candidate, check []Candidate) {
	agree := make([]Candidate, 0, len(cs))
	for _, c := range cs {
		if Agrees(in, c) {
			agree = append(agree, c)
		}
	}
	if len(agree) > 0 {
		cs = agree
	}
	cs = mergeCandidates(cs)
	switch len(cs) {
	case 0:
		return nil, nil
	case 1:
		return &cs[0], nil
	}
	return nil, cs[:min(len(cs), MaxCandidates)]
}

// --- the service side ---------------------------------------------------------------

// cached looks the input up in geo_cache: its own key first (a pin or a
// normal / cleaned-line result), then the location's area-retry entry
// (corrected=true). pgx.ErrNoRows when neither is there.
func (s *Service) cached(ctx context.Context, in locateIn) (c models.GeoCache, corrected bool, err error) {
	if in.key == "" {
		return c, false, pgx.ErrNoRows
	}
	c, err = s.q.GetGeoCache(ctx, in.key)
	if !errors.Is(err, pgx.ErrNoRows) || in.locationID == uuid.Nil {
		return c, false, err
	}
	c, err = s.q.GetGeoCache(ctx, AreaCacheKey(in.locationID, in.key))
	return c, err == nil, err
}

// storeArea is the location's search box (StoreArea): its map bbox, else
// the square around store (or the location's own pin when store is nil).
func (s *Service) storeArea(ctx context.Context, locationID uuid.UUID, store *Point) (BBox, bool) {
	var bbox []float64
	if l, err := s.tenancy.GetLocation(ctx, locationID); err == nil && l != nil {
		bbox = l.MapBBox
		if store == nil && l.Lat != nil && l.Lng != nil {
			store = &Point{Lat: *l.Lat, Lng: *l.Lng}
		}
	} else if err != nil {
		s.logger.Warn().Ctx(ctx).Err(err).Str("location_id", locationID.String()).Msg("geo: location lookup for the search area failed")
	}
	return StoreArea(bbox, store)
}

// geocoded is one locate's geocoder answer: a place (res, cached under
// cacheKey unless ""), or several to pick from (check).
type geocoded struct {
	res       GeocodeResult
	check     []Candidate
	corrected bool
	cacheKey  string
}

// geocode is the normal geocode plus the 2.2.2 retries (see the top of this
// file). ErrNotFound when every step found nothing; any other error is a
// provider failure (the caller retries the whole thing later).
func (s *Service) geocode(ctx context.Context, prov *Providers, in locateIn) (geocoded, error) {
	decide := func(a Address, g GeocodeResult) geocoded {
		cands := g.Candidates
		if len(cands) == 0 {
			cands = []Candidate{{Lat: g.Lat, Lng: g.Lng, Quality: g.Quality}}
		}
		if !Confident(a, cands) {
			return geocoded{check: cands[:min(len(cands), MaxCandidates)]}
		}
		return geocoded{res: g, cacheKey: in.key}
	}
	g, err := prov.Geocode(ctx, in.addr, in.store)
	if err == nil {
		return decide(in.addr, g), nil
	}
	if !errors.Is(err, ErrNotFound) {
		return geocoded{}, err
	}

	// 1. The street part only, same town + zip.
	street := CleanStreet(in.addr.Line1)
	if street != "" && normWords(street) != normWords(stripUnit(in.addr.Line1)) {
		a := in.addr
		a.Line1 = street
		g, err := prov.Geocode(ctx, a, in.store)
		if err == nil {
			s.logger.Info().Ctx(ctx).Msg("geo: found once the notes after the street were dropped")
			return decide(a, g), nil
		}
		if !errors.Is(err, ErrNotFound) {
			return geocoded{}, err
		}
	}

	// 2. Street + state only, inside the store's area. Only a line with a
	// house number: a bare name searched in a 20-mile box is a guess.
	if HouseNumber(street) == "" || in.locationID == uuid.Nil {
		return geocoded{}, ErrNotFound
	}
	box, ok := s.storeArea(ctx, in.locationID, in.store)
	if !ok {
		return geocoded{}, ErrNotFound
	}
	a := Address{Line1: street, State: in.addr.State}
	r, err := geocodeIn(ctx, prov.Geocoder, a, box)
	if err != nil {
		return geocoded{}, err
	}
	one, check := areaPick(a, r.Candidates)
	switch {
	case one != nil:
		key := ""
		if in.key != "" {
			key = AreaCacheKey(in.locationID, in.key)
		}
		s.logger.Info().Ctx(ctx).Str("source", r.Source).Msg("geo: found in the store's area without the town + zip (corrected)")
		return geocoded{res: GeocodeResult{Point: one.Point(), Quality: one.Quality, Source: r.Source, Candidates: []Candidate{*one}},
			corrected: true, cacheKey: key}, nil
	case len(check) > 0:
		s.logger.Info().Ctx(ctx).Int("candidates", len(check)).Msg("geo: several places in the store's area (check)")
		return geocoded{check: check}, nil
	}
	return geocoded{}, ErrNotFound
}
