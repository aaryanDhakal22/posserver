package geo

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DefaultGoogleURL is the Google Maps Platform web-service base.
const DefaultGoogleURL = "https://maps.googleapis.com/maps/api"

// Google geocodes and routes with Google Maps Platform (Geocoding,
// Directions, Distance Matrix). Chosen instead of the free stack when
// GOOGLE_MAPS_API_KEY is set — no app change needed to switch.
type Google struct {
	BaseURL string
	Key     string
	Client  *http.Client
}

// Name — see Provider.
func (g *Google) Name() string { return "google" }

func (g *Google) get(ctx context.Context, api string, q url.Values, out any) error {
	q.Set("key", g.Key)
	return getJSON(ctx, g.Client, trimSlash(g.BaseURL)+"/"+api+"/json?"+q.Encode(), nil, out)
}

func latlng(p Point) string {
	return strconv.FormatFloat(p.Lat, 'f', 6, 64) + "," + strconv.FormatFloat(p.Lng, 'f', 6, 64)
}

type googleGeocode struct {
	Status  string `json:"status"`
	Results []struct {
		FormattedAddress  string `json:"formatted_address"`
		AddressComponents []struct {
			LongName  string   `json:"long_name"`
			ShortName string   `json:"short_name"`
			Types     []string `json:"types"`
		} `json:"address_components"`
		PartialMatch bool `json:"partial_match"`
		Geometry     struct {
			Location struct {
				Lat float64 `json:"lat"`
				Lng float64 `json:"lng"`
			} `json:"location"`
			LocationType string `json:"location_type"`
		} `json:"geometry"`
	} `json:"results"`
	ErrorMessage string `json:"error_message"`
}

// Geocode — see Geocoder.
func (g *Google) Geocode(ctx context.Context, a Address, near *Point) (GeocodeResult, error) {
	q := url.Values{"address": {a.Line()}, "components": {"country:US"}}
	if near != nil {
		const d = 0.25
		q.Set("bounds", fmt.Sprintf("%.5f,%.5f|%.5f,%.5f", near.Lat-d, near.Lng-d, near.Lat+d, near.Lng+d))
	}
	var body googleGeocode
	if err := g.get(ctx, "geocode", q, &body); err != nil {
		return GeocodeResult{}, fmt.Errorf("google geocode: %w", err)
	}
	return parseGoogleGeocode(body)
}

func parseGoogleGeocode(body googleGeocode) (GeocodeResult, error) {
	switch body.Status {
	case "OK":
	case "ZERO_RESULTS":
		return GeocodeResult{}, ErrNotFound
	default:
		return GeocodeResult{}, fmt.Errorf("google geocode: %s %s", body.Status, body.ErrorMessage)
	}
	cs := make([]Candidate, 0, len(body.Results))
	for _, r := range body.Results {
		q := QualityApprox
		if !r.PartialMatch && (r.Geometry.LocationType == "ROOFTOP" || r.Geometry.LocationType == "RANGE_INTERPOLATED") {
			q = QualityExact
		}
		comp := map[string]string{}
		for _, c := range r.AddressComponents {
			for _, t := range c.Types {
				if _, ok := comp[t]; ok {
					continue
				}
				if t == "administrative_area_level_1" || t == "route" {
					comp[t] = c.ShortName
				} else {
					comp[t] = c.LongName
				}
			}
		}
		line1 := strings.TrimSpace(comp["street_number"] + " " + comp["route"])
		city := firstNonEmpty(comp["locality"], comp["sublocality"], comp["neighborhood"], comp["administrative_area_level_3"])
		c := newCandidate(line1, city, comp["administrative_area_level_1"], comp["postal_code"],
			Point{Lat: r.Geometry.Location.Lat, Lng: r.Geometry.Location.Lng}, q)
		if line1 == "" && r.FormattedAddress != "" {
			c.Label = strings.TrimSuffix(r.FormattedAddress, ", USA")
		}
		cs = append(cs, c)
	}
	return resultFrom(cs, "google")
}

type googleDirections struct {
	Status string `json:"status"`
	Routes []struct {
		OverviewPolyline struct {
			Points string `json:"points"`
		} `json:"overview_polyline"`
		Legs []struct {
			Distance struct {
				Value int `json:"value"`
			} `json:"distance"`
			Duration struct {
				Value int `json:"value"`
			} `json:"duration"`
		} `json:"legs"`
		WaypointOrder []int `json:"waypoint_order"`
	} `json:"routes"`
	ErrorMessage string `json:"error_message"`
}

func (g *Google) directions(ctx context.Context, from, to Point, via []Point, optimize bool) (googleDirections, error) {
	q := url.Values{"origin": {latlng(from)}, "destination": {latlng(to)}, "mode": {"driving"}}
	if len(via) > 0 {
		parts := make([]string, 0, len(via)+1)
		if optimize {
			parts = append(parts, "optimize:true")
		}
		for _, p := range via {
			parts = append(parts, latlng(p))
		}
		q.Set("waypoints", strings.Join(parts, "|"))
	}
	var body googleDirections
	if err := g.get(ctx, "directions", q, &body); err != nil {
		return body, fmt.Errorf("google directions: %w", err)
	}
	switch body.Status {
	case "OK":
		if len(body.Routes) == 0 {
			return body, ErrNoRoute
		}
		return body, nil
	case "ZERO_RESULTS", "NOT_FOUND":
		return body, ErrNoRoute
	}
	return body, fmt.Errorf("google directions: %s %s", body.Status, body.ErrorMessage)
}

// Route — see Router.
func (g *Google) Route(ctx context.Context, from, to Point) (RouteResult, error) {
	body, err := g.directions(ctx, from, to, nil, false)
	if err != nil {
		return RouteResult{}, err
	}
	r := body.Routes[0]
	out := RouteResult{Polyline: r.OverviewPolyline.Points}
	for _, l := range r.Legs {
		out.DistanceM += l.Distance.Value
		out.DurationS += l.Duration.Value
	}
	return out, nil
}

type googleMatrix struct {
	Status string `json:"status"`
	Rows   []struct {
		Elements []struct {
			Status   string `json:"status"`
			Duration struct {
				Value int `json:"value"`
			} `json:"duration"`
		} `json:"elements"`
	} `json:"rows"`
	ErrorMessage string `json:"error_message"`
}

// Table — see Router (Distance Matrix).
func (g *Google) Table(ctx context.Context, origins, dests []Point) ([][]int, error) {
	if len(origins) == 0 || len(dests) == 0 {
		return nil, nil
	}
	join := func(ps []Point) string {
		s := make([]string, len(ps))
		for i, p := range ps {
			s[i] = latlng(p)
		}
		return strings.Join(s, "|")
	}
	var body googleMatrix
	q := url.Values{"origins": {join(origins)}, "destinations": {join(dests)}, "mode": {"driving"}}
	if err := g.get(ctx, "distancematrix", q, &body); err != nil {
		return nil, fmt.Errorf("google matrix: %w", err)
	}
	if body.Status != "OK" {
		return nil, fmt.Errorf("google matrix: %s %s", body.Status, body.ErrorMessage)
	}
	if len(body.Rows) != len(origins) {
		return nil, fmt.Errorf("google matrix: %d rows, want %d", len(body.Rows), len(origins))
	}
	out := make([][]int, len(origins))
	for i, row := range body.Rows {
		if len(row.Elements) != len(dests) {
			return nil, fmt.Errorf("google matrix: row %d has %d cells", i, len(row.Elements))
		}
		out[i] = make([]int, len(dests))
		for j, e := range row.Elements {
			if e.Status != "OK" {
				out[i][j] = -1
			} else {
				out[i][j] = e.Duration.Value
			}
		}
	}
	return out, nil
}

// Trip — see Router. Directions needs a fixed destination, so the run ends
// at the stop farthest from the store and the rest are optimized as waypoints.
func (g *Google) Trip(ctx context.Context, from Point, stops []Point) (TripResult, error) {
	if len(stops) == 0 {
		return TripResult{}, nil
	}
	last := 0
	for i, p := range stops {
		if HaversineM(from, p) > HaversineM(from, stops[last]) {
			last = i
		}
	}
	via, viaIdx := []Point{}, []int{}
	for i, p := range stops {
		if i != last {
			via, viaIdx = append(via, p), append(viaIdx, i)
		}
	}
	body, err := g.directions(ctx, from, stops[last], via, true)
	if err != nil {
		return TripResult{}, err
	}
	r := body.Routes[0]
	out := TripResult{Polyline: r.OverviewPolyline.Points}
	for _, l := range r.Legs {
		out.DistanceM += l.Distance.Value
		out.DurationS += l.Duration.Value
	}
	if len(r.WaypointOrder) == len(via) {
		for _, w := range r.WaypointOrder {
			out.Order = append(out.Order, viaIdx[w])
		}
	} else {
		out.Order = append(out.Order, viaIdx...)
	}
	out.Order = append(out.Order, last)
	return out, nil
}
