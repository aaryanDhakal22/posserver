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
		{"street-level, far apart, only the top in the input's town + zip", []Candidate{c("Taylor Ave", "21286", 39.1), c("Taylor Ave", "21204", 39.2)}, true},
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

// offset moves p north by m meters (1 deg lat ≈ 111.2 km).
func offset(p Point, m float64) Point { return Point{Lat: p.Lat + m/111195, Lng: p.Lng} }

// The same house returned twice must not send a normal order to check.
func TestConfidenceCollapsesTheSameAddress(t *testing.T) {
	home := Point{Lat: 39.3982, Lng: -76.5895}
	at := func(line1, city, zip string, p Point) Candidate {
		return newCandidate(line1, city, "MD", zip, p, QualityExact)
	}
	withZip := Address{Line1: "931 Taylor Ave", City: "Towson", State: "MD", Zip: "21286"}
	noZip := Address{Line1: "931 Taylor Ave", City: "Towson", State: "MD"}
	cases := []struct {
		name string
		cs   []Candidate
		left int // after collapse
	}{
		{"Ave vs Avenue ~20 m apart", []Candidate{
			at("931 Taylor Ave", "Towson", "21286", home), at("931 Taylor Avenue", "Towson", "21286", offset(home, 20))}, 1},
		{"Nominatim node (no zip) + its building", []Candidate{
			at("931 Taylor Avenue", "Towson", "21286", home), at("931 Taylor Avenue", "Towson", "", offset(home, 35))}, 1},
		{"E-prefixed duplicate within 150 m", []Candidate{
			at("931 E Taylor Ave", "Towson", "21286", home), at("931 Taylor Ave", "Towson", "21286", offset(home, 120))}, 1},
		{"identical line + zip, points 600 m apart", []Candidate{
			at("931 Taylor Ave", "Towson", "21286", home), at("931 TAYLOR AVE", "Towson", "21286", offset(home, 600))}, 1},
		{"duplicate collapsed, a different address kept", []Candidate{
			at("931 Taylor Ave", "Towson", "21286", home), at("931 Taylor Avenue", "Towson", "21286", offset(home, 20)),
			at("913 Taylor Ave", "Towson", "21286", offset(home, 200))}, 2},
	}
	for _, tc := range cases {
		if got := collapse(tc.cs); len(got) != tc.left || got[0] != tc.cs[0] {
			t.Errorf("%s: collapse = %+v", tc.name, got)
		}
		for _, in := range []Address{withZip, noZip} {
			if !Confident(in, tc.cs) {
				t.Errorf("%s: input %+v must be confident", tc.name, in)
			}
		}
	}

	// Zips disagree but the points are one house (a zip boundary): not
	// collapsed (the zip differs), yet an input without a zip is confident
	// — every agreeing candidate is within 150 m of the top.
	split := []Candidate{at("931 Taylor Ave", "Towson", "21286", home), at("931 Taylor Ave", "Towson", "21204", offset(home, 40))}
	if len(collapse(split)) != 2 || !Confident(noZip, split) {
		t.Errorf("zip boundary: collapse %d, confident %v", len(collapse(split)), Confident(noZip, split))
	}

	// The provider path collapses too: a check result never offers the same
	// house twice.
	r, err := resultFrom([]Candidate{
		at("931 Taylor Ave", "Towson", "21286", home), at("931 Taylor Avenue", "Towson", "21286", offset(home, 20)),
		at("931 W Taylor Ave", "Towson", "21204", Point{Lat: 39.4021, Lng: -76.6181})}, "fake")
	if err != nil || len(r.Candidates) != 2 || r.Candidates[1].Line1 != "931 W Taylor Ave" {
		t.Fatalf("resultFrom = %+v, %v", r.Candidates, err)
	}
}

// Genuinely different addresses still need staff.
func TestConfidenceKeepsRealAmbiguity(t *testing.T) {
	at := func(line1, city, zip string, lat, lng float64) Candidate {
		return newCandidate(line1, city, "MD", zip, Point{Lat: lat, Lng: lng}, QualityExact)
	}
	twoTowns := []Candidate{at("100 Main St", "Annapolis", "21401", 38.9774, -76.4878), at("100 Main St", "Laurel", "20707", 39.1094, -76.8572)}
	if len(collapse(twoTowns)) != 2 {
		t.Error("two towns must not collapse")
	}
	if Confident(Address{Line1: "100 Main St", State: "MD"}, twoTowns) {
		t.Error("100 Main St in two towns, no zip: check")
	}
	if !Confident(Address{Line1: "100 Main St", City: "Annapolis", State: "MD", Zip: "21401"}, twoTowns) {
		t.Error("the zip picks the top one: confident")
	}
	// Same zip, same number + street name, other direction, 2.5 km apart.
	ew := []Candidate{at("931 E Taylor Ave", "Towson", "21286", 39.3982, -76.5895), at("931 W Taylor Ave", "Towson", "21286", 39.4021, -76.6181)}
	if len(collapse(ew)) != 2 || Confident(Address{Line1: "931 Taylor Ave", City: "Towson", State: "MD", Zip: "21286"}, ew) {
		t.Error("E vs W Taylor in one zip, far apart: check")
	}
	// Two towns with identical lines but no zips: not the same address.
	noZips := []Candidate{at("100 Main Street", "Annapolis", "", 38.9776, -76.4881), at("100 Main Street", "Laurel", "", 39.1094, -76.8572)}
	if len(collapse(noZips)) != 2 || Confident(Address{Line1: "100 Main St", State: "MD"}, noZips) {
		t.Error("identical lines without zips, far apart: check")
	}
}

// Staging 2026-10-25: an online delivery whose house the geocoders can't find
// comes back as several street-level points of the input's own road (Nominatim
// ways). That is an approximate location (2.0.9 placed it as approx), not a
// choice for staff — and identical labels are offered only once.
func TestStreetLevelMatchesOfTheInputStreetAreConfident(t *testing.T) {
	at := func(line1, zip string, lat, lng float64) Candidate {
		return newCandidate(line1, "Towson", "MD", zip, Point{Lat: lat, Lng: lng}, QualityApprox)
	}
	dulaney := []Candidate{at("Dulaney Valley Road", "21204", 39.4027006, -76.6020055),
		at("Dulaney Valley Road", "21204", 39.4097999, -76.6005963), at("Dulaney Valley Road", "21204", 39.4025591, -76.601907)}
	if !Confident(Address{Line1: "204 Dulaney Valley Rd", City: "Towson", State: "MD", Zip: "21204"}, dulaney) {
		t.Error("street-level points of the input's road: confident (approx)")
	}
	if got := len(collapse(dulaney)); got != 1 {
		t.Errorf("identical labels collapse to one, got %d", got)
	}
	cromwell := []Candidate{at("Cromwell Bridge Road", "21286", 39.3996736, -76.5775528),
		at("Cromwell Bridge Road", "21284", 39.4013787, -76.5702961)}
	if !Confident(Address{Line1: "1764 Cromwell Bridge Rd", City: "Towson", State: "MD", Zip: "21286"}, cromwell) {
		t.Error("the same road across a zip line, 0.7 km apart: confident")
	}
	// Another road, or the same road name far away: still check.
	other := []Candidate{at("Cromwell Bridge Road", "21286", 39.3996, -76.5775), at("Bridge Road", "21286", 39.41, -76.57)}
	if Confident(Address{Line1: "1764 Cromwell Bridge Rd", Zip: "21286"}, other) {
		t.Error("street-level match of another street: check")
	}
	far := []Candidate{at("Main Street", "21286", 39.39, -76.57), at("Main Street", "21157", 39.57, -76.99)}
	if Confident(Address{Line1: "12 Main St"}, far) {
		t.Error("the same street name 40 km apart: check")
	}
	// A long road through three towns (Putty Hill Ave, staging 2026-10-25):
	// the top match in the input's town + zip is used; without a town, check.
	putty := []Candidate{
		newCandidate("Putty Hill Avenue", "Parkville", "MD", "21234", Point{Lat: 39.3806534, Lng: -76.529737}, QualityApprox),
		newCandidate("Putty Hill Avenue", "Carney", "MD", "21234", Point{Lat: 39.3797595, Lng: -76.521974}, QualityApprox),
		newCandidate("Putty Hill Avenue", "Towson", "MD", "21234", Point{Lat: 39.391643, Lng: -76.565511}, QualityApprox)}
	if !Confident(Address{Line1: "1774 Putty Hill Ave", City: "Parkville", State: "MD", Zip: "21234"}, putty) {
		t.Error("long road, top in the input's town + zip: confident")
	}
	if Confident(Address{Line1: "1774 Putty Hill Ave", State: "MD", Zip: "21234"}, putty) {
		t.Error("long road, no town given: check")
	}
}
