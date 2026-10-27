package geo

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultNominatimURL is OpenStreetMap's public Nominatim search endpoint.
const DefaultNominatimURL = "https://nominatim.openstreetmap.org/search"

// Nominatim geocodes with OpenStreetMap's Nominatim — the fallback when the
// Census geocoder has no match. Its usage policy wants an identifying
// User-Agent with a contact and at most 1 request per second (enforced here
// for the whole process).
type Nominatim struct {
	BaseURL string
	Contact string
	Client  *http.Client
}

// nominatimGate is the process-wide 1 req/s limiter.
var nominatimGate = &minInterval{every: time.Second}

type minInterval struct {
	mu    sync.Mutex
	every time.Duration
	last  time.Time
}

// wait blocks until a request may go out (or ctx ends).
func (m *minInterval) wait(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d := m.every - time.Since(m.last); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	m.last = time.Now()
	return nil
}

type nominatimHit struct {
	Lat     string `json:"lat"`
	Lon     string `json:"lon"`
	Type    string `json:"type"`
	Address struct {
		HouseNumber string `json:"house_number"`
		Road        string `json:"road"`
		City        string `json:"city"`
		Town        string `json:"town"`
		Village     string `json:"village"`
		Hamlet      string `json:"hamlet"`
		Suburb      string `json:"suburb"`
		State       string `json:"state"`
		StateCode   string `json:"ISO3166-2-lvl4"` // "US-MD"
		Postcode    string `json:"postcode"`
	} `json:"address"`
}

// nominatimLimit is how many matches Nominatim is asked for (candidates).
const nominatimLimit = 5

// UserAgent is the identifying UA Nominatim's policy asks for.
func (n *Nominatim) UserAgent() string {
	c := strings.TrimSpace(n.Contact)
	if c == "" {
		c = "unset"
	}
	return "QuiccPOS/2.0 (contact: " + c + ")"
}

// Geocode — see Geocoder. near biases (not bounds) the search to ~0.25° around it.
func (n *Nominatim) Geocode(ctx context.Context, a Address, near *Point) (GeocodeResult, error) {
	q := url.Values{}
	q.Set("format", "jsonv2")
	q.Set("addressdetails", "1")
	q.Set("limit", strconv.Itoa(nominatimLimit))
	q.Set("countrycodes", "us")
	if a.Line1 != "" && (a.City != "" || a.Zip != "") {
		q.Set("street", stripUnit(a.Line1))
		q.Set("city", a.City)
		q.Set("state", a.State)
		q.Set("postalcode", a.Zip)
	} else {
		q.Set("q", a.Line())
	}
	if near != nil {
		const d = 0.25
		q.Set("viewbox", fmt.Sprintf("%.5f,%.5f,%.5f,%.5f", near.Lng-d, near.Lat+d, near.Lng+d, near.Lat-d))
	}
	return n.search(ctx, q, a)
}

// GeocodeIn — see AreaGeocoder: a free-form search ("street, ST") bounded to
// the box (viewbox + bounded=1).
func (n *Nominatim) GeocodeIn(ctx context.Context, a Address, box BBox) (GeocodeResult, error) {
	a.City, a.Zip = "", ""
	q := url.Values{}
	q.Set("format", "jsonv2")
	q.Set("addressdetails", "1")
	q.Set("limit", strconv.Itoa(nominatimLimit))
	q.Set("countrycodes", "us")
	q.Set("q", a.Line())
	q.Set("viewbox", fmt.Sprintf("%.5f,%.5f,%.5f,%.5f", box.MinLng, box.MaxLat, box.MaxLng, box.MinLat))
	q.Set("bounded", "1")
	return n.search(ctx, q, a)
}

// search runs one request through the process-wide 1 req/s gate.
func (n *Nominatim) search(ctx context.Context, q url.Values, a Address) (GeocodeResult, error) {
	if err := nominatimGate.wait(ctx); err != nil {
		return GeocodeResult{}, err
	}
	var hits []nominatimHit
	hdr := http.Header{"User-Agent": []string{n.UserAgent()}}
	if err := getJSON(ctx, n.Client, n.BaseURL+"?"+q.Encode(), hdr, &hits); err != nil {
		return GeocodeResult{}, fmt.Errorf("nominatim: %w", err)
	}
	return parseNominatim(hits, a)
}

func parseNominatim(hits []nominatimHit, a Address) (GeocodeResult, error) {
	in := HouseNumber(a.Line1)
	if in == "" {
		in = HouseNumber(a.Oneline)
	}
	cs := make([]Candidate, 0, len(hits))
	for _, h := range hits {
		lat, err1 := strconv.ParseFloat(h.Lat, 64)
		lng, err2 := strconv.ParseFloat(h.Lon, 64)
		if err1 != nil || err2 != nil {
			if len(cs) == 0 && len(hits) == 1 {
				return GeocodeResult{}, fmt.Errorf("nominatim: bad coordinates %q,%q", h.Lat, h.Lon)
			}
			continue
		}
		q := QualityApprox
		if in != "" && strings.EqualFold(strings.TrimSpace(h.Address.HouseNumber), in) {
			q = QualityExact
		}
		ad := h.Address
		line1 := strings.TrimSpace(ad.HouseNumber + " " + ad.Road)
		city := firstNonEmpty(ad.City, ad.Town, ad.Village, ad.Hamlet, ad.Suburb)
		state := ad.State
		if code, ok := strings.CutPrefix(ad.StateCode, "US-"); ok && code != "" {
			state = code
		}
		zip := ad.Postcode
		if z := zip5.FindString(zip); z != "" {
			zip = z
		}
		cs = append(cs, newCandidate(line1, city, state, zip, Point{Lat: lat, Lng: lng}, q))
	}
	return resultFrom(cs, "nominatim")
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
