package geo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// serve answers every request with the fixture (and status), recording the
// last request.
func serve(t *testing.T, status int, name string, last *atomic.Pointer[http.Request]) *httptest.Server {
	t.Helper()
	body := fixture(t, name)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if last != nil {
			last.Store(r)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNormalizeKey(t *testing.T) {
	cases := []struct {
		a, b Address
	}{
		{Address{Line1: "584 Broadway", City: "Bayonne", State: "NJ", Zip: "07002"},
			Address{Line1: "584  BROADWAY.", City: "bayonne", State: "nj", Zip: "07002-1234"}},
		{Address{Line1: "12 West 33rd Street", City: "Bayonne", State: "NJ", Zip: "07002"},
			Address{Line1: "12 W. 33rd St", City: "Bayonne", State: "NJ", Zip: "07002"}},
		{Address{Line1: "100 Avenue C Apt 4B", City: "Bayonne", State: "NJ", Zip: "07002"},
			Address{Line1: "100 Ave C", City: "Bayonne", State: "NJ", Zip: "07002"}},
		{Address{Line1: "7 Kennedy Boulevard #2", City: "Bayonne", State: "NJ", Zip: "07002"},
			Address{Line1: "7 Kennedy Blvd", City: "Bayonne", State: "NJ", Zip: "07002"}},
	}
	for _, c := range cases {
		if ka, kb := NormalizeKey(c.a), NormalizeKey(c.b); ka != kb {
			t.Errorf("keys differ:\n %q\n %q", ka, kb)
		}
	}
	if got := NormalizeKey(Address{Line1: "584 Broadway", City: "Bayonne", State: "NJ", Zip: "07002"}); got != "584 broadway|bayonne|nj|07002" {
		t.Errorf("key = %q", got)
	}
	for in, want := range map[string]string{
		"100 Ave C, Apt. 4B": "100 Ave C", "7 Kennedy Blvd #2": "7 Kennedy Blvd", "5 Main St Unit 3": "5 Main St",
		"12 Westeron Ave": "12 Westeron Ave", "40 Forest Floor Rd": "40 Forest Floor Rd", "9 Elm St Fl 2": "9 Elm St", "3 Oak St Apt B": "3 Oak St",
	} {
		if got := stripUnit(in); got != want {
			t.Errorf("stripUnit(%q) = %q, want %q", in, got, want)
		}
	}
	if NormalizeKey(Address{Line1: "1 Main St", Zip: "07002"}) == NormalizeKey(Address{Line1: "2 Main St", Zip: "07002"}) {
		t.Error("different house numbers share a key")
	}
}

func TestStoreAddress(t *testing.T) {
	a := StoreAddress("584 Broadway", "Bayonne, NJ 07002")
	if a.City != "Bayonne" || a.State != "NJ" || a.Zip != "07002" || a.Oneline != "" {
		t.Errorf("parsed %+v", a)
	}
	b := StoreAddress("584 Broadway", "Bayonne New Jersey")
	if b.Oneline != "584 Broadway, Bayonne New Jersey" {
		t.Errorf("oneline %+v", b)
	}
	if got := (Address{Line1: "100 Ave C Apt 4", City: "Bayonne", State: "NJ", Zip: "07002"}).Line(); got != "100 Ave C, Bayonne, NJ 07002" {
		t.Errorf("Line() = %q", got)
	}
}

func TestHaversineAndOutsideArea(t *testing.T) {
	store := Point{Lat: 40.6666, Lng: -74.1158}
	// ~1 degree of latitude is ~111.2 km.
	if d := HaversineM(Point{Lat: 40, Lng: -74}, Point{Lat: 41, Lng: -74}); d < 111000 || d > 111400 {
		t.Errorf("1 deg lat = %.0f m", d)
	}
	near := Point{Lat: 40.6690, Lng: -74.1143} // ~300 m
	far := Point{Lat: 40.7357, Lng: -74.1724}  // Newark, ~9 km
	if OutsideArea(store, near, 8047) {
		t.Error("near is outside")
	}
	if !OutsideArea(store, far, 8047) {
		t.Error("far is inside")
	}
	if OutsideArea(store, far, 0) {
		t.Error("radius 0 must disable the tag")
	}
}

func TestBackoffSchedule(t *testing.T) {
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for i, w := range want {
		if got := Backoff(i + 1); got != w {
			t.Errorf("Backoff(%d) = %v, want %v", i+1, got, w)
		}
	}
	created := time.Date(2026, 10, 22, 18, 0, 0, 0, time.UTC)
	if _, ok := NextTry(created, created.Add(time.Minute), 1); !ok {
		t.Error("first retry refused")
	}
	if _, ok := NextTry(created, created.Add(5*time.Hour+45*time.Minute), 20); ok {
		t.Error("retry past 6h allowed")
	}
}

func TestCensusParsing(t *testing.T) {
	var last atomic.Pointer[http.Request]
	srv := serve(t, 200, "census_match.json", &last)
	c := &Census{BaseURL: srv.URL}
	r, err := c.Geocode(context.Background(), Address{Line1: "584 Broadway", City: "Bayonne", State: "NJ", Zip: "07002"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Quality != QualityExact || r.Source != "census" || r.Lat < 40.66 || r.Lat > 40.67 || r.Lng > -74.11 {
		t.Errorf("result %+v", r)
	}
	q := last.Load().URL.Query()
	if q.Get("benchmark") != "Public_AR_Current" || q.Get("format") != "json" || q.Get("address") != "584 Broadway, Bayonne, NJ 07002" {
		t.Errorf("query %v", q)
	}

	// A match on another house number (range end) is approx.
	r, err = (&Census{BaseURL: serve(t, 200, "census_range.json", nil).URL}).Geocode(context.Background(),
		Address{Line1: "9999 Broadway", City: "Bayonne", State: "NJ", Zip: "07002"}, nil)
	if err != nil || r.Quality != QualityApprox {
		t.Errorf("range match: %+v %v", r, err)
	}

	_, err = (&Census{BaseURL: serve(t, 200, "census_nomatch.json", nil).URL}).Geocode(context.Background(), Address{Line1: "1 Nowhere Ln"}, nil)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("no match err = %v", err)
	}
	_, err = (&Census{BaseURL: serve(t, 502, "census_nomatch.json", nil).URL}).Geocode(context.Background(), Address{Line1: "1 Main St"}, nil)
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("5xx must be a provider failure, got %v", err)
	}
}

func TestNominatimParsingAndPolicy(t *testing.T) {
	var last atomic.Pointer[http.Request]
	srv := serve(t, 200, "nominatim_hit.json", &last)
	n := &Nominatim{BaseURL: srv.URL, Contact: "owner@example.com"}
	store := &Point{Lat: 40.6666, Lng: -74.1158}
	r, err := n.Geocode(context.Background(), Address{Line1: "12 W 33rd St", City: "Bayonne", State: "NJ", Zip: "07002"}, store)
	if err != nil {
		t.Fatal(err)
	}
	if r.Quality != QualityExact || r.Source != "nominatim" || r.Lat != 40.6689524 {
		t.Errorf("result %+v", r)
	}
	req := last.Load()
	if ua := req.Header.Get("User-Agent"); ua != "QuiccPOS/2.0 (contact: owner@example.com)" {
		t.Errorf("UA = %q", ua)
	}
	q := req.URL.Query()
	if q.Get("format") != "jsonv2" || q.Get("countrycodes") != "us" || q.Get("street") != "12 W 33rd St" ||
		q.Get("city") != "Bayonne" || q.Get("postalcode") != "07002" || q.Get("viewbox") == "" {
		t.Errorf("query %v", q)
	}

	r, err = (&Nominatim{BaseURL: serve(t, 200, "nominatim_street.json", nil).URL}).Geocode(context.Background(),
		Address{Line1: "12 W 33rd St", City: "Bayonne", State: "NJ", Zip: "07002"}, nil)
	if err != nil || r.Quality != QualityApprox {
		t.Errorf("street-level hit: %+v %v", r, err)
	}
}

func TestNominatimRateLimit(t *testing.T) {
	g := &minInterval{every: 150 * time.Millisecond}
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := g.wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if el := time.Since(start); el < 300*time.Millisecond {
		t.Errorf("3 requests in %v, limiter not applied", el)
	}
}

func TestChainFallsBackAndClassifies(t *testing.T) {
	ctx := context.Background()
	census := &Census{BaseURL: serve(t, 200, "census_nomatch.json", nil).URL}
	nom := &Nominatim{BaseURL: serve(t, 200, "nominatim_hit.json", nil).URL}
	r, err := Chain{census, nom}.Geocode(ctx, Address{Line1: "12 W 33rd St", City: "Bayonne", State: "NJ", Zip: "07002"}, nil)
	if err != nil || r.Source != "nominatim" {
		t.Errorf("fallback: %+v %v", r, err)
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("[]")) }))
	defer empty.Close()
	_, err = Chain{census, &Nominatim{BaseURL: empty.URL}}.Geocode(ctx, Address{Line1: "1 Nowhere Ln"}, nil)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("both no-match: %v", err)
	}
	down := &Census{BaseURL: serve(t, 503, "census_nomatch.json", nil).URL}
	_, err = Chain{down, &Nominatim{BaseURL: empty.URL}}.Geocode(ctx, Address{Line1: "1 Nowhere Ln"}, nil)
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("a failed provider + no match must retry, got %v", err)
	}
}

func TestOSRMParsing(t *testing.T) {
	ctx := context.Background()
	var last atomic.Pointer[http.Request]
	o := &OSRM{BaseURL: serve(t, 200, "osrm_route.json", &last).URL + "/"}
	r, err := o.Route(ctx, Point{Lat: 40.666587, Lng: -74.115832}, Point{Lat: 40.668952, Lng: -74.114274})
	if err != nil {
		t.Fatal(err)
	}
	if r.DistanceM != 3702 || r.DurationS != 411 || r.Polyline == "" {
		t.Errorf("route %+v", r)
	}
	u := last.Load().URL
	if u.Path != "/route/v1/driving/-74.115832,40.666587;-74.114274,40.668952" ||
		u.Query().Get("overview") != "full" || u.Query().Get("geometries") != "polyline" {
		t.Errorf("route url %v", u)
	}

	o = &OSRM{BaseURL: serve(t, 200, "osrm_table.json", &last).URL}
	tbl, err := o.Table(ctx, []Point{{1, 1}}, []Point{{2, 2}, {3, 3}, {4, 4}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl) != 1 || tbl[0][0] != 432 || tbl[0][1] != -1 || tbl[0][2] != 288 {
		t.Errorf("table %v", tbl)
	}
	q := last.Load().URL.Query()
	if q.Get("sources") != "0" || q.Get("destinations") != "1;2;3" || q.Get("annotations") != "duration" {
		t.Errorf("table query %v", q)
	}

	o = &OSRM{BaseURL: serve(t, 200, "osrm_trip.json", &last).URL}
	trip, err := o.Trip(ctx, Point{Lat: 40.66, Lng: -74.11}, []Point{{40.67, -74.12}, {40.65, -74.10}, {40.68, -74.13}})
	if err != nil {
		t.Fatal(err)
	}
	// waypoint_index: A=3, B=1, C=2 → B, C, A.
	if len(trip.Order) != 3 || trip.Order[0] != 1 || trip.Order[1] != 2 || trip.Order[2] != 0 || trip.DistanceM != 7400 || trip.DurationS != 901 {
		t.Errorf("trip %+v", trip)
	}
	q = last.Load().URL.Query()
	if q.Get("source") != "first" || q.Get("roundtrip") != "false" || q.Get("destination") != "any" {
		t.Errorf("trip query %v", q)
	}

	_, err = (&OSRM{BaseURL: serve(t, 400, "osrm_noroute.json", nil).URL}).Route(ctx, Point{1, 1}, Point{2, 2})
	if !errors.Is(err, ErrNoRoute) {
		t.Errorf("NoRoute err = %v", err)
	}
	_, err = (&OSRM{BaseURL: "http://127.0.0.1:1"}).Route(ctx, Point{1, 1}, Point{2, 2})
	if err == nil || errors.Is(err, ErrNoRoute) {
		t.Errorf("unreachable must be a provider failure, got %v", err)
	}
}

func TestProviderSelection(t *testing.T) {
	p := Build(Config{})
	if p.Name() != "census+nominatim/none" || p.Routing() {
		t.Errorf("default = %s routing=%v", p.Name(), p.Routing())
	}
	if _, err := p.Route(context.Background(), Point{1, 1}, Point{2, 2}); !errors.Is(err, ErrRoutingDisabled) {
		t.Errorf("no router err = %v", err)
	}
	p = Build(Config{OSRMURL: "http://osrm:5000"})
	if p.Name() != "census+nominatim/osrm" || !p.Routing() {
		t.Errorf("osrm = %s", p.Name())
	}
	if _, ok := p.Router.(*OSRM); !ok {
		t.Errorf("router %T", p.Router)
	}
	p = Build(Config{OSRMURL: "http://osrm:5000", GoogleKey: "k"})
	if p.Name() != "google" || !p.Routing() {
		t.Errorf("google = %s", p.Name())
	}
	if _, ok := p.Geocoder.(*Google); !ok {
		t.Errorf("geocoder %T", p.Geocoder)
	}
}

func TestGoogleGeocode(t *testing.T) {
	var last atomic.Pointer[http.Request]
	srv := serve(t, 200, "google_geocode.json", &last)
	g := &Google{BaseURL: srv.URL, Key: "secret"}
	r, err := g.Geocode(context.Background(), Address{Line1: "584 Broadway", City: "Bayonne", State: "NJ", Zip: "07002"}, nil)
	if err != nil || r.Quality != QualityExact || r.Source != "google" {
		t.Errorf("%+v %v", r, err)
	}
	u := last.Load().URL
	if !strings.HasSuffix(u.Path, "/geocode/json") || u.Query().Get("key") != "secret" {
		t.Errorf("url %v", u)
	}
}
