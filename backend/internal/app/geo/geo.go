// Package geo locates delivery orders and routes them from the store (2.0.9).
//
// After a delivery order is persisted, main enqueues an order_geo row and the
// worker (Service.Run) works it in the background — ingest never waits:
//
//  1. geocode the address (geo_cache first; a pinned entry always wins), via
//     the US Census geocoder with Nominatim as the fallback — or Google when
//     GOOGLE_MAPS_API_KEY is set;
//  2. route store → address (OSRM at GEO_OSRM_URL, or Google), and tag it
//     outside_area when it is farther (straight line) than the store's radius;
//  3. push RPC order.geo to the location's agent with the drive times to and
//     from the store and every other open located delivery (the Pi's AUTO
//     planner works offline from them).
//
// Provider failures retry with backoff for up to 6 h; "no match" is final
// (state not_found — staff drop a pin on the tablet, which arrives as RPC
// geo.pin and is remembered in geo_cache for that address).
package geo

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strings"
	"time"
)

// Point is a WGS84 coordinate.
type Point struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// Address is what is geocoded. Oneline, when set, is used as-is for the
// free-form providers (the store's own address is two free lines).
type Address struct {
	Line1, City, State, Zip string
	Oneline                 string
}

// Line is the one-line form "line1, city, state zip".
func (a Address) Line() string {
	if a.Oneline != "" {
		return a.Oneline
	}
	parts := []string{}
	if l := stripUnit(a.Line1); l != "" {
		parts = append(parts, l)
	}
	if a.City != "" {
		parts = append(parts, a.City)
	}
	sz := strings.TrimSpace(a.State + " " + a.Zip)
	if sz != "" {
		parts = append(parts, sz)
	}
	return strings.Join(parts, ", ")
}

// Geocode qualities (order_geo.quality / geo_cache.quality).
const (
	QualityExact  = "exact"
	QualityApprox = "approx"
	QualityPinned = "pinned"
)

// Order geo states.
const (
	StatePending  = "pending"
	StateLocated  = "located"
	StateNotFound = "not_found"
)

// GeocodeResult is one provider's answer.
type GeocodeResult struct {
	Point
	Quality string
	Source  string // census | nominatim | google
}

// RouteResult is one driving route.
type RouteResult struct {
	DistanceM int
	DurationS int
	Polyline  string // Google encoded polyline, precision 5
}

// TripResult is a stop order from the first point (the store) through stops.
type TripResult struct {
	Order     []int // indexes into the stops passed in, in driving order
	Polyline  string
	DistanceM int
	DurationS int
}

var (
	// ErrNotFound: every geocoder answered, none matched (final).
	ErrNotFound = errors.New("address not found")
	// ErrRoutingDisabled: no router configured (GEO_OSRM_URL empty, no Google key).
	ErrRoutingDisabled = errors.New("routing disabled")
	// ErrNoRoute: the router answered but found no route between the points.
	ErrNoRoute = errors.New("no route")
)

// Geocoder turns an address into a point. near (the store) biases the search.
// ErrNotFound means no match; any other error is a provider failure (retry).
type Geocoder interface {
	Geocode(ctx context.Context, a Address, near *Point) (GeocodeResult, error)
}

// Router answers driving questions.
type Router interface {
	Route(ctx context.Context, from, to Point) (RouteResult, error)
	// Table returns durations in seconds [len(origins)][len(dests)]; -1 = no route.
	Table(ctx context.Context, origins, dests []Point) ([][]int, error)
	// Trip orders stops for a run starting at from (no return leg).
	Trip(ctx context.Context, from Point, stops []Point) (TripResult, error)
}

// Provider is a geocoder + a router.
type Provider interface {
	Geocoder
	Router
	Name() string
}

// earthRadiusM is the mean Earth radius.
const earthRadiusM = 6371008.8

// HaversineM is the great-circle distance between a and b in meters.
func HaversineM(a, b Point) float64 {
	rad := math.Pi / 180
	dLat := (b.Lat - a.Lat) * rad
	dLng := (b.Lng - a.Lng) * rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(a.Lat*rad)*math.Cos(b.Lat*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * earthRadiusM * math.Asin(math.Min(1, math.Sqrt(h)))
}

// OutsideArea: farther than radiusM (straight line) from the store.
func OutsideArea(store, p Point, radiusM int) bool {
	if radiusM <= 0 {
		return false
	}
	return HaversineM(store, p) > float64(radiusM)
}

// --- address normalization -------------------------------------------------

var (
	nonWord   = regexp.MustCompile(`[^a-z0-9#]+`)
	unitRe    = regexp.MustCompile(`(?i)((\s*,\s*|\s+)(apt|apartment|unit|suite|ste|fl|floor|rm|room)(\.\s*|\s+)([a-z0-9-]*\d[a-z0-9-]*|[a-z])|\s*,?\s*#\s*([a-z0-9-]*\d[a-z0-9-]*|[a-z]))\s*$`)
	houseNum  = regexp.MustCompile(`^\s*(\d+[a-zA-Z]?)\b`)
	zip5      = regexp.MustCompile(`\d{5}`)
	cityStZip = regexp.MustCompile(`^\s*([^,]+?)\s*,\s*([A-Za-z]{2})\.?\s*(\d{5})?(?:-\d{4})?\s*$`)
)

// suffixes maps street words to their USPS abbreviation so "Main Street" and
// "Main St." share a cache key.
var suffixes = map[string]string{
	"street": "st", "str": "st", "avenue": "ave", "av": "ave", "boulevard": "blvd",
	"road": "rd", "drive": "dr", "lane": "ln", "court": "ct", "place": "pl",
	"terrace": "ter", "parkway": "pkwy", "highway": "hwy", "square": "sq",
	"circle": "cir", "expressway": "expy", "turnpike": "tpke", "plaza": "plz",
	"north": "n", "south": "s", "east": "e", "west": "w",
	"northeast": "ne", "northwest": "nw", "southeast": "se", "southwest": "sw",
	"first": "1st", "second": "2nd", "third": "3rd", "fourth": "4th", "fifth": "5th",
	"sixth": "6th", "seventh": "7th", "eighth": "8th", "ninth": "9th", "tenth": "10th",
}

// stripUnit drops a trailing apartment / unit designator ("Apt 4B", "#2").
func stripUnit(line1 string) string {
	return strings.TrimSpace(unitRe.ReplaceAllString(strings.TrimSpace(line1), ""))
}

func normWords(s string) string {
	s = nonWord.ReplaceAllString(strings.ToLower(s), " ")
	words := strings.Fields(s)
	for i, w := range words {
		if a, ok := suffixes[w]; ok {
			words[i] = a
		}
	}
	return strings.Join(words, " ")
}

// NormalizeKey is the geo_cache key: line1|city|state|zip lowercased with
// punctuation + whitespace collapsed, street suffixes abbreviated, the unit
// dropped and the zip cut to 5 digits.
func NormalizeKey(a Address) string {
	if a.Oneline != "" && a.Line1 == "" {
		return "line|" + normWords(a.Oneline)
	}
	z := zip5.FindString(a.Zip)
	return normWords(stripUnit(a.Line1)) + "|" + normWords(a.City) + "|" + normWords(a.State) + "|" + z
}

// HouseNumber is the leading house number of a street line ("" if none).
func HouseNumber(line string) string {
	m := houseNum.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

// StoreAddress builds the geocodable address of a store from its two free
// address lines (line2 usually "City, ST 12345").
func StoreAddress(line1, line2 string) Address {
	a := Address{Line1: strings.TrimSpace(line1)}
	if m := cityStZip.FindStringSubmatch(line2); m != nil {
		a.City, a.State, a.Zip = m[1], strings.ToUpper(m[2]), m[3]
		return a
	}
	a.Oneline = strings.TrimSpace(strings.Trim(strings.TrimSpace(line1)+", "+strings.TrimSpace(line2), ", "))
	return a
}

// --- retry schedule ------------------------------------------------------------

// GiveUpAfter is how long a provider failure is retried before the order is
// marked not_found (staff can still drop a pin).
const GiveUpAfter = 6 * time.Hour

var backoffSteps = []time.Duration{
	30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute,
	10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
}

// Backoff is the wait after the n-th failed attempt (1-based): 30 s, 1, 2, 5,
// 10, 15 min, then every 30 min.
func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(backoffSteps) {
		return backoffSteps[len(backoffSteps)-1]
	}
	return backoffSteps[attempt-1]
}

// NextTry is when to retry after the attempt-th failure of a row created at
// created; ok=false means give up (past GiveUpAfter).
func NextTry(created, now time.Time, attempt int) (time.Time, bool) {
	next := now.Add(Backoff(attempt))
	if next.Sub(created) > GiveUpAfter {
		return time.Time{}, false
	}
	return next, true
}
