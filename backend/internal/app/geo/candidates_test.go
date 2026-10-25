package geo

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
)

// census_multi.json / nominatim_multi.json are real answers for "100 Main St,
// MD" (Census trimmed to its first 4 of 24 matches); google_multi.json is
// shaped like a Geocoding API answer with two matches.

func TestCensusCandidates(t *testing.T) {
	c := &Census{BaseURL: serve(t, 200, "census_multi.json", nil).URL}
	in := Address{Line1: "100 Main St", State: "MD"}
	r, err := c.Geocode(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Candidates) != 4 {
		t.Fatalf("candidates = %d: %+v", len(r.Candidates), r.Candidates)
	}
	top := r.Candidates[0]
	want := Candidate{Label: "100 Main St, Accident, MD 21520", Line1: "100 Main St", City: "Accident", State: "MD",
		PostalCode: "21520", Lat: 39.628967469673, Lng: -79.319845856288, Quality: QualityExact}
	if top != want {
		t.Errorf("top = %+v\nwant %+v", top, want)
	}
	if r.Point != top.Point() || r.Quality != QualityExact || r.Source != "census" {
		t.Errorf("result = %+v", r)
	}
	if r.Candidates[1].City != "Annapolis" || r.Candidates[1].PostalCode != "21401" {
		t.Errorf("second = %+v", r.Candidates[1])
	}
	// Same house + street in four towns and no zip given: ambiguous.
	if Confident(in, r.Candidates) {
		t.Error("4 towns must not be confident without a zip")
	}
	// The zip picks Annapolis — but it is not the top match, so still check.
	if Confident(Address{Line1: "100 Main St", State: "MD", Zip: "21401"}, r.Candidates) {
		t.Error("top disagrees on zip: must not be confident")
	}
	// Reordered so the zip's match is first: confident.
	cs := append([]Candidate{r.Candidates[1]}, r.Candidates[0], r.Candidates[2], r.Candidates[3])
	if !Confident(Address{Line1: "100 Main Street", City: "Annapolis", State: "MD", Zip: "21401-1234"}, cs) {
		t.Error("top agrees on house + street + zip and no other does: confident")
	}
}

func TestNominatimCandidates(t *testing.T) {
	srv := serve(t, 200, "nominatim_multi.json", nil)
	r, err := (&Nominatim{BaseURL: srv.URL}).Geocode(context.Background(), Address{Line1: "100 Main Street", State: "MD"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Candidates) != 5 || r.Source != "nominatim" {
		t.Fatalf("candidates = %+v", r.Candidates)
	}
	c := r.Candidates[0]
	if c.Line1 != "100 Main Street" || c.City != "Annapolis" || c.State != "MD" || c.PostalCode != "21403" ||
		c.Label != "100 Main Street, Annapolis, MD 21403" || c.Lat != 38.9776335 || c.Quality != QualityExact {
		t.Errorf("top = %+v", c)
	}
	if r.Candidates[2].City != "Laurel" || r.Candidates[4].City != "Westernport" {
		t.Errorf("order = %+v", r.Candidates)
	}
	if Confident(Address{Line1: "100 Main St", State: "MD"}, r.Candidates) {
		t.Error("five towns: not confident")
	}
	// Two Annapolis hits ~20 m apart with different zips: the zip decides.
	if !Confident(Address{Line1: "100 Main St", City: "Annapolis", State: "MD", Zip: "21403"}, r.Candidates) {
		t.Error("top agrees on zip 21403 and nobody else does: confident")
	}
}

func TestNominatimAsksForFive(t *testing.T) {
	var last atomic.Pointer[http.Request]
	srv := serve(t, 200, "nominatim_multi.json", &last)
	if _, err := (&Nominatim{BaseURL: srv.URL}).Geocode(context.Background(), Address{Line1: "100 Main St", City: "Annapolis", State: "MD", Zip: "21401"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := last.Load().URL.Query().Get("limit"); got != "5" {
		t.Errorf("limit = %q", got)
	}
}

func TestGoogleCandidates(t *testing.T) {
	r, err := (&Google{BaseURL: serve(t, 200, "google_multi.json", nil).URL, Key: "k"}).Geocode(context.Background(),
		Address{Line1: "931 Taylor Ave", City: "Towson", State: "MD", Zip: "21286"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Candidates) != 2 {
		t.Fatalf("candidates = %+v", r.Candidates)
	}
	a, b := r.Candidates[0], r.Candidates[1]
	if a.Line1 != "931 E Taylor Ave" || a.City != "Towson" || a.State != "MD" || a.PostalCode != "21286" ||
		a.Label != "931 E Taylor Ave, Towson, MD 21286" || a.Quality != QualityApprox {
		t.Errorf("first = %+v", a)
	}
	if b.Line1 != "931 W Taylor Ave" || b.PostalCode != "21204" {
		t.Errorf("second = %+v", b)
	}
	// "Taylor Ave" agrees with "E Taylor Ave" (direction ignored); the zip
	// rules out W Taylor: confident.
	if !Confident(Address{Line1: "931 Taylor Ave", City: "Towson", State: "MD", Zip: "21286"}, r.Candidates) {
		t.Error("expected confident")
	}
	// Without a zip both agree: check.
	if Confident(Address{Line1: "931 Taylor Ave", City: "Towson", State: "MD"}, r.Candidates) {
		t.Error("expected check")
	}
}

func TestConfidenceRule(t *testing.T) {
	c := func(line1, zip string, lat float64) Candidate {
		return newCandidate(line1, "Towson", "MD", zip, Point{Lat: lat, Lng: -76.6}, QualityExact)
	}
	in := Address{Line1: "931 Taylor Avenue Apt 2", City: "Towson", State: "MD", Zip: "21286"}
	cases := []struct {
		name string
		cs   []Candidate
		want bool
	}{
		{"none", nil, false},
		{"exactly one (even a different street)", []Candidate{c("12 Elm St", "21204", 39.1)}, true},
		{"top agrees, others differ", []Candidate{c("931 E Taylor Ave", "21286", 39.1), c("931 W Taylor Ave", "21204", 39.2), c("913 Taylor Ave", "21286", 39.3)}, true},
		{"two agree", []Candidate{c("931 Taylor Ave", "21286", 39.1), c("931 Taylor Rd", "21286", 39.2)}, false},
		{"top has another house number", []Candidate{c("939 Taylor Ave", "21286", 39.1), c("931 Taylor Ave", "21204", 39.2)}, false},
		{"top has another street", []Candidate{c("931 Tyler Ave", "21286", 39.1), c("931 Taylor Ave", "21204", 39.2)}, false},
		{"top has another zip", []Candidate{c("931 Taylor Ave", "21204", 39.1), c("931 Taylor Ave", "21093", 39.2)}, false},
		{"street-level matches only", []Candidate{c("Taylor Ave", "21286", 39.1), c("Taylor Ave", "21204", 39.2)}, false},
	}
	for _, tc := range cases {
		if got := Confident(in, tc.cs); got != tc.want {
			t.Errorf("%s: Confident = %v, want %v", tc.name, got, tc.want)
		}
	}
	// No house number in the input: only a single match is confident.
	if Confident(Address{Line1: "Taylor Ave", Zip: "21286"}, []Candidate{c("1 Taylor Ave", "21286", 39.1), c("2 Taylor Ave", "21204", 39.2)}) {
		t.Error("no house number: must not be confident with 2 matches")
	}
}

func TestStreetNameAndDedupe(t *testing.T) {
	for in, want := range map[string]string{
		"931 E. Taylor Avenue":     "taylor",
		"931 Taylor Ave Apt 4":     "taylor",
		"12 West 33rd Street":      "33rd",
		"100 Avenue C":             "c",
		"5 N Ave":                  "n ave",
		"7 Kennedy Blvd #2":        "kennedy",
		"931 TAYLOR AVE":           "taylor",
		"1600 Pennsylvania Ave NW": "pennsylvania",
	} {
		if got := StreetName(in); got != want {
			t.Errorf("StreetName(%q) = %q, want %q", in, got, want)
		}
	}
	cs := dedupe([]Candidate{
		{Line1: "a", Lat: 39.123401, Lng: -76.5},
		{Line1: "b", Lat: 39.123449, Lng: -76.50001}, // same point at 4 decimals
		{Line1: "c", Lat: 39.1240, Lng: -76.5},
	})
	if len(cs) != 2 || cs[0].Line1 != "a" || cs[1].Line1 != "c" {
		t.Errorf("dedupe = %+v", cs)
	}
	if titleCase("931 E TAYLOR AVE") != "931 E Taylor Ave" {
		t.Errorf("titleCase = %q", titleCase("931 E TAYLOR AVE"))
	}
}
