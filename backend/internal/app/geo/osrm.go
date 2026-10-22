package geo

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// OSRM routes with a self-hosted OSRM (GEO_OSRM_URL, car profile, MLD) that
// covers the store area. It has no geocoder; Providers pairs it with
// Census + Nominatim.
type OSRM struct {
	BaseURL string
	Client  *http.Client
}

type osrmResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Routes  []struct {
		Geometry string  `json:"geometry"`
		Distance float64 `json:"distance"`
		Duration float64 `json:"duration"`
	} `json:"routes"`
	Durations [][]*float64 `json:"durations"`
	Trips     []struct {
		Geometry string  `json:"geometry"`
		Distance float64 `json:"distance"`
		Duration float64 `json:"duration"`
	} `json:"trips"`
	Waypoints []struct {
		WaypointIndex int `json:"waypoint_index"`
		TripsIndex    int `json:"trips_index"`
	} `json:"waypoints"`
}

func coords(pts ...Point) string {
	parts := make([]string, len(pts))
	for i, p := range pts {
		parts[i] = strconv.FormatFloat(p.Lng, 'f', 6, 64) + "," + strconv.FormatFloat(p.Lat, 'f', 6, 64)
	}
	return strings.Join(parts, ";")
}

func (o *OSRM) get(ctx context.Context, service string, pts []Point, q url.Values) (osrmResponse, error) {
	var body osrmResponse
	u := trimSlash(o.BaseURL) + "/" + service + "/v1/driving/" + coords(pts...) + "?" + q.Encode()
	status, err := getJSONAny(ctx, o.Client, u, nil, &body)
	if err != nil {
		return body, fmt.Errorf("osrm %s: %w", service, err)
	}
	switch body.Code {
	case "Ok":
		return body, nil
	case "NoRoute", "NoSegment", "NoTrips", "NoMatch":
		return body, ErrNoRoute
	}
	return body, fmt.Errorf("osrm %s: http %d code %q %s", service, status, body.Code, body.Message)
}

// Route — see Router.
func (o *OSRM) Route(ctx context.Context, from, to Point) (RouteResult, error) {
	q := url.Values{"overview": {"full"}, "geometries": {"polyline"}}
	body, err := o.get(ctx, "route", []Point{from, to}, q)
	if err != nil {
		return RouteResult{}, err
	}
	return parseOSRMRoute(body)
}

func parseOSRMRoute(body osrmResponse) (RouteResult, error) {
	if len(body.Routes) == 0 {
		return RouteResult{}, ErrNoRoute
	}
	r := body.Routes[0]
	return RouteResult{DistanceM: round(r.Distance), DurationS: round(r.Duration), Polyline: r.Geometry}, nil
}

// Table — see Router.
func (o *OSRM) Table(ctx context.Context, origins, dests []Point) ([][]int, error) {
	if len(origins) == 0 || len(dests) == 0 {
		return nil, nil
	}
	pts := append(append([]Point{}, origins...), dests...)
	src, dst := make([]string, len(origins)), make([]string, len(dests))
	for i := range origins {
		src[i] = strconv.Itoa(i)
	}
	for j := range dests {
		dst[j] = strconv.Itoa(len(origins) + j)
	}
	q := url.Values{"annotations": {"duration"}, "sources": {strings.Join(src, ";")}, "destinations": {strings.Join(dst, ";")}}
	body, err := o.get(ctx, "table", pts, q)
	if err != nil {
		return nil, err
	}
	return parseOSRMTable(body, len(origins), len(dests))
}

func parseOSRMTable(body osrmResponse, n, m int) ([][]int, error) {
	if len(body.Durations) != n {
		return nil, fmt.Errorf("osrm table: %d rows, want %d", len(body.Durations), n)
	}
	out := make([][]int, n)
	for i, row := range body.Durations {
		if len(row) != m {
			return nil, fmt.Errorf("osrm table: row %d has %d cells, want %d", i, len(row), m)
		}
		out[i] = make([]int, m)
		for j, v := range row {
			if v == nil {
				out[i][j] = -1
			} else {
				out[i][j] = round(*v)
			}
		}
	}
	return out, nil
}

// Trip — see Router. The store is the fixed first point; the trip ends at
// whichever stop is best (no return leg).
func (o *OSRM) Trip(ctx context.Context, from Point, stops []Point) (TripResult, error) {
	if len(stops) == 0 {
		return TripResult{}, nil
	}
	q := url.Values{"source": {"first"}, "roundtrip": {"false"}, "destination": {"any"},
		"geometries": {"polyline"}, "overview": {"full"}}
	body, err := o.get(ctx, "trip", append([]Point{from}, stops...), q)
	if err != nil {
		return TripResult{}, err
	}
	return parseOSRMTrip(body, len(stops))
}

func parseOSRMTrip(body osrmResponse, nStops int) (TripResult, error) {
	if len(body.Trips) == 0 || len(body.Waypoints) != nStops+1 {
		return TripResult{}, ErrNoRoute
	}
	// waypoints are in input order; waypoint_index is each one's place in
	// the trip. Input 0 is the store.
	idx := make([]int, nStops)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return body.Waypoints[idx[a]+1].WaypointIndex < body.Waypoints[idx[b]+1].WaypointIndex
	})
	t := body.Trips[0]
	return TripResult{Order: idx, Polyline: t.Geometry, DistanceM: round(t.Distance), DurationS: round(t.Duration)}, nil
}

func round(f float64) int { return int(math.Round(f)) }

// noRouter is the Router when routing is off (GEO_OSRM_URL empty, no Google key).
type noRouter struct{}

func (noRouter) Route(context.Context, Point, Point) (RouteResult, error) {
	return RouteResult{}, ErrRoutingDisabled
}
func (noRouter) Table(context.Context, []Point, []Point) ([][]int, error) {
	return nil, ErrRoutingDisabled
}
func (noRouter) Trip(context.Context, Point, []Point) (TripResult, error) {
	return TripResult{}, ErrRoutingDisabled
}
