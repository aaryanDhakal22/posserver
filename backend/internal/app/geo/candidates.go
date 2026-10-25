package geo

import (
	"math"
	"strings"
)

// Candidate is one geocoder match (2.1.0). Every provider returns all of its
// matches, best first; the worker decides from them whether the address is
// confident (located) or needs a staff pick (state check).
type Candidate struct {
	Label      string  `json:"label"` // formatted "line1, city, ST zip"
	Line1      string  `json:"line1"`
	City       string  `json:"city"`
	State      string  `json:"state"`
	PostalCode string  `json:"postal_code"`
	Lat        float64 `json:"lat"`
	Lng        float64 `json:"lng"`
	Quality    string  `json:"-"` // exact | approx (the top one is the result's)
}

// Point is the candidate's coordinate.
func (c Candidate) Point() Point { return Point{Lat: c.Lat, Lng: c.Lng} }

// ConfirmedAddress is the address staff confirmed by picking a candidate
// (order.geo confirmed_address).
type ConfirmedAddress struct {
	Line1      string `json:"line1"`
	City       string `json:"city"`
	State      string `json:"state"`
	PostalCode string `json:"postal_code"`
}

// Confirmed is the candidate's address parts.
func (c Candidate) Confirmed() ConfirmedAddress {
	return ConfirmedAddress{Line1: c.Line1, City: c.City, State: c.State, PostalCode: c.PostalCode}
}

// MaxCandidates is how many candidates a check result carries.
const MaxCandidates = 3

// newCandidate fills Label from the parts.
func newCandidate(line1, city, state, zip string, p Point, quality string) Candidate {
	c := Candidate{Line1: strings.TrimSpace(line1), City: strings.TrimSpace(city), State: strings.TrimSpace(state),
		PostalCode: strings.TrimSpace(zip), Lat: p.Lat, Lng: p.Lng, Quality: quality}
	c.Label = formatLabel(c)
	return c
}

func formatLabel(c Candidate) string {
	parts := []string{}
	for _, s := range []string{c.Line1, c.City} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	if sz := strings.TrimSpace(c.State + " " + c.PostalCode); sz != "" {
		parts = append(parts, sz)
	}
	return strings.Join(parts, ", ")
}

// dedupe drops candidates at the same point (rounded to 4 decimals, ~11 m),
// keeping the first (best) one.
func dedupe(cs []Candidate) []Candidate {
	seen := map[[2]int64]bool{}
	out := make([]Candidate, 0, len(cs))
	for _, c := range cs {
		k := [2]int64{int64(math.Round(c.Lat * 1e4)), int64(math.Round(c.Lng * 1e4))}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, c)
	}
	return out
}

// resultFrom builds a GeocodeResult from deduplicated candidates (the top one
// is the result's point + quality). ErrNotFound when there are none.
func resultFrom(cs []Candidate, source string) (GeocodeResult, error) {
	cs = dedupe(cs)
	if len(cs) == 0 {
		return GeocodeResult{}, ErrNotFound
	}
	return GeocodeResult{Point: cs[0].Point(), Quality: cs[0].Quality, Source: source, Candidates: cs}, nil
}

// Confident is the rule that lets main place an address without asking staff:
// exactly one candidate, or the top candidate agrees with the input on house
// number + street name + zip and no other candidate does.
func Confident(in Address, cs []Candidate) bool {
	switch len(cs) {
	case 0:
		return false
	case 1:
		return true
	}
	if !Agrees(in, cs[0]) {
		return false
	}
	for _, c := range cs[1:] {
		if Agrees(in, c) {
			return false
		}
	}
	return true
}

// Agrees: the candidate has the input's house number, street name (ignoring
// direction words, the street type and case/punctuation) and zip (an input
// with no zip can't disagree on it).
func Agrees(in Address, c Candidate) bool {
	line := in.Line1
	if line == "" {
		line = in.Oneline
	}
	hn := HouseNumber(line)
	if hn == "" || HouseNumber(c.Line1) != hn {
		return false
	}
	s := StreetName(line)
	if s == "" || s != StreetName(c.Line1) {
		return false
	}
	if z := zip5.FindString(in.Zip); z != "" && zip5.FindString(c.PostalCode) != z {
		return false
	}
	return true
}

// streetNoise are the words StreetName drops: directions and street types
// (already abbreviated by normWords).
var streetNoise = map[string]bool{
	"n": true, "s": true, "e": true, "w": true, "ne": true, "nw": true, "se": true, "sw": true,
	"st": true, "ave": true, "blvd": true, "rd": true, "dr": true, "ln": true, "ct": true, "pl": true,
	"ter": true, "pkwy": true, "hwy": true, "sq": true, "cir": true, "expy": true, "tpke": true, "plz": true,
	"way": true, "trl": true, "aly": true,
}

// StreetName is the street's name proper from a street line: "12 W. 33rd
// Street Apt 4" → "33rd". Falls back to every word when only noise is left
// ("100 Avenue C" keeps "c"; "5 N Ave" keeps "n ave").
func StreetName(line string) string {
	words := strings.Fields(normWords(stripUnit(line)))
	if len(words) > 0 && HouseNumber(words[0]) == words[0] {
		words = words[1:]
	}
	kept := []string{}
	for _, w := range words {
		if !streetNoise[w] {
			kept = append(kept, w)
		}
	}
	if len(kept) == 0 {
		kept = words
	}
	return strings.Join(kept, " ")
}

// titleCase turns "584 BROADWAY" into "584 Broadway" (the Census answers in
// capitals).
func titleCase(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
