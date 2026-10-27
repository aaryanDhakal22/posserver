package geo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// areaProvider answers Geocode by the whole one-line address (so a wrong zip
// or a note misses, as at the real providers) and GeocodeIn by the town-less
// line, returning everything it knows — in or out of the box — so the box
// filter is what is tested.
type areaProvider struct {
	mu    sync.Mutex
	name  string
	full  map[string][]Candidate
	area  map[string][]Candidate
	calls []string
	fail  error // GeocodeIn answers this
}

func newAreaProvider(name string) *areaProvider {
	return &areaProvider{name: name, full: map[string][]Candidate{}, area: map[string][]Candidate{}}
}

func (p *areaProvider) Geocode(_ context.Context, a Address, _ *Point) (GeocodeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	line := strings.ToLower(a.Line())
	p.calls = append(p.calls, "full:"+line)
	return resultFrom(p.full[line], p.name)
}

func (p *areaProvider) GeocodeIn(_ context.Context, a Address, _ BBox) (GeocodeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	line := strings.ToLower(a.Line())
	p.calls = append(p.calls, "area:"+line)
	if p.fail != nil {
		return GeocodeResult{}, p.fail
	}
	return resultFrom(p.area[line], p.name)
}

func (p *areaProvider) takeCalls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.calls
	p.calls = nil
	return c
}

// useGeocoder swaps the harness geocoder (routing stays the straight-line fake).
func (h *harness) useGeocoder(g Geocoder) {
	h.svc.prov.Store(&Providers{Geocoder: g, Router: h.prov, name: "fake", routing: true})
}

// The two addresses from the store on 2026-10-26: the POS sends Towson 21286
// (its default) for both, with a note after the street.
var (
	wadsworth    = cand("1613 Wadsworth Way", "Baltimore", "21239", 39.3622852, -76.5820168)
	yorkRd       = cand("5415 York Road", "Baltimore", "21212", 39.3561930, -76.6096973)
	farWadsworth = cand("1613 Wadsworth Way", "Frederick", "21701", 39.4100, -77.4100) // ~70 km west
)

func onlineDelivery(h *harness, line1 string) uuid.UUID {
	id := uuid.New()
	h.db.addOrder(id, order.Address{Line1: line1, City: "Towson", State: "MD", PostalCode: "21286"})
	if _, err := h.svc.q.InsertOrderGeo(context.Background(), insertParams(h, id)); err != nil {
		h.t.Fatal(err)
	}
	return id
}

func keyOf(line1 string) string {
	return NormalizeKey(Address{Line1: line1, City: "Towson", State: "MD", Zip: "21286"})
}

func TestCleanStreet(t *testing.T) {
	for in, want := range map[string]string{
		"1613 Wadsworth Way, HOUSE":      "1613 Wadsworth Way",
		"5415 YORK RD, AROMA PIZZA":      "5415 YORK RD",
		"Apt 4, 12 Main St":              "12 Main St",
		"12 Main St Apt 4B, back door":   "12 Main St",
		"  12   Main St  ":               "12 Main St",
		"Towson University, dorm":        "Towson University, dorm",
		"931 Taylor Ave":                 "931 Taylor Ave",
		"931 Taylor Ave, #2, side porch": "931 Taylor Ave",
	} {
		if got := CleanStreet(in); got != want {
			t.Errorf("CleanStreet(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStoreArea(t *testing.T) {
	m := []float64{-76.7, 39.3, -76.5, 39.5}
	b, ok := StoreArea(m, &testStore)
	if !ok || b != (BBox{MinLng: -76.7, MinLat: 39.3, MaxLng: -76.5, MaxLat: 39.5}) {
		t.Fatalf("map bbox: %+v %v", b, ok)
	}
	for _, bad := range [][]float64{nil, {1, 2, 3}, {-76.5, 39.3, -76.7, 39.5}, {-76.7, 39.5, -76.5, 39.5}} {
		b, ok := StoreArea(bad, &testStore)
		if !ok || b != BoxAround(testStore, AreaHalfSideM) {
			t.Errorf("%v: want the square around the store, got %+v %v", bad, b, ok)
		}
	}
	if _, ok := StoreArea(nil, nil); ok {
		t.Error("no map and no store pin: no area")
	}
	b = BoxAround(testStore, AreaHalfSideM)
	north := func(mi float64) Point { return Point{Lat: testStore.Lat + mi*1609.344/111195.0, Lng: testStore.Lng} }
	if !b.Contains(north(9.5)) || b.Contains(north(10.5)) || !b.Contains(wadsworth.Point()) || b.Contains(farWadsworth.Point()) {
		t.Errorf("box %+v", b)
	}
	if !b.Contains(Point{Lat: testStore.Lat, Lng: testStore.Lng + 0.18}) || b.Contains(Point{Lat: testStore.Lat, Lng: testStore.Lng + 0.2}) {
		t.Errorf("the box's half width is 10 mi at Towson's latitude: %+v", b)
	}
}

func TestWithState(t *testing.T) {
	for in, want := range map[string]string{
		"1613 Wadsworth Way":           "1613 Wadsworth Way, MD",
		"1613 Wadsworth Way, MD 21239": "1613 Wadsworth Way, MD 21239",
		"1613 Wadsworth Way md":        "1613 Wadsworth Way md",
		"1613 Maryland Ave":            "1613 Maryland Ave, MD",
	} {
		if got := withState(in, "MD"); got != want {
			t.Errorf("withState(%q) = %q, want %q", in, got, want)
		}
	}
	if got := withState("1 Main St", ""); got != "1 Main St" {
		t.Errorf("no state: %q", got)
	}
}

func TestAreaRetryNotesStripped(t *testing.T) {
	h := newHarness(t)
	p := newAreaProvider("fake")
	h.useGeocoder(p)
	// Only the cleaned line matches (with its own town + zip).
	towson := cand("100 York Rd", "Towson", "21286", 39.4010, -76.6010)
	p.full["100 york rd, towson, md 21286"] = []Candidate{towson}
	id := onlineDelivery(h, "100 YORK RD, AROMA PIZZA")

	h.drain()
	push := onlyPush(t, h, id)
	if push.State != StateLocated || *push.Lat != towson.Lat || push.Quality != QualityExact || push.Corrected {
		t.Fatalf("push = %+v (a note dropped is not a correction)", push)
	}
	want := []string{"full:100 york rd, aroma pizza, towson, md 21286", "full:100 york rd, towson, md 21286"}
	if got := p.takeCalls(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	if _, ok := h.db.cache[keyOf("100 YORK RD, AROMA PIZZA")]; !ok {
		t.Error("cleaned-line result not cached for the original input")
	}
}

func TestAreaRetryWrongZipDroppedIsCorrected(t *testing.T) {
	h := newHarness(t)
	p := newAreaProvider("fake")
	h.useGeocoder(p)
	// Inside the store's area once; the same street far away is discarded.
	p.area["1613 wadsworth way, md"] = []Candidate{wadsworth, farWadsworth}
	id := onlineDelivery(h, "1613 Wadsworth Way, HOUSE")

	h.drain()
	push := onlyPush(t, h, id)
	if push.State != StateLocated || *push.Lat != wadsworth.Lat || *push.Lng != wadsworth.Lng || push.Quality != QualityExact || !push.Corrected {
		t.Fatalf("push = %+v", push)
	}
	if push.Candidates != nil || push.Confirmed || push.StoreToS == nil {
		t.Errorf("push = %+v", push)
	}
	want := []string{
		"full:1613 wadsworth way, house, towson, md 21286",
		"full:1613 wadsworth way, towson, md 21286",
		"area:1613 wadsworth way, md",
	}
	if got := p.takeCalls(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	raw, _ := json.Marshal(push)
	if !strings.Contains(string(raw), `"corrected":true`) {
		t.Errorf("wire: %s", raw)
	}

	// Cached for the original input, scoped to this location only.
	key := keyOf("1613 Wadsworth Way, HOUSE")
	if _, ok := h.db.cache[key]; ok {
		t.Error("an area result must not be cached under the global key")
	}
	if c, ok := h.db.cache[AreaCacheKey(h.scope.LocationID, key)]; !ok || c.Lat != wadsworth.Lat {
		t.Fatalf("area cache = %+v %v", c, ok)
	}
	if _, _, err := h.svc.cached(context.Background(), locateIn{key: key, locationID: uuid.New()}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("another location must not see this location's corrected result: %v", err)
	}

	// The same bad input again: instant, from the cache, still corrected.
	id2 := onlineDelivery(h, "1613 Wadsworth Way, HOUSE")
	h.drain()
	if p2 := onlyPush(t, h, id2); p2.State != StateLocated || !p2.Corrected || *p2.Lat != wadsworth.Lat {
		t.Errorf("cached push = %+v", p2)
	}
	if got := p.takeCalls(); len(got) != 0 {
		t.Errorf("cache hit called the providers: %v", got)
	}

	// The store moved: re-routed from the cache, still corrected.
	h.svc.StoreMoved(context.Background(), h.scope.TenantID, h.scope.LocationID)
	h.drain()
	ps := h.agent.take()
	if len(ps) != 2 {
		t.Fatalf("re-route pushes = %+v", ps)
	}
	for _, p := range ps {
		if !p.Corrected {
			t.Errorf("re-route lost corrected: %+v", p)
		}
	}

	// A staff pin for that address wins and is not "corrected".
	h.rpc(h.svc.HandlePin, map[string]any{"order_id": id.String(), "lat": 39.36, "lng": -76.58})
	h.drain()
	if p := onlyPush(t, h, id); p.Quality != QualityPinned || p.Corrected {
		t.Errorf("pinned push = %+v", p)
	}
}

func TestAreaRetryOutsideTheBoxIsRejected(t *testing.T) {
	h := newHarness(t)
	p := newAreaProvider("fake")
	h.useGeocoder(p)
	p.area["1613 wadsworth way, md"] = []Candidate{farWadsworth}
	id := onlineDelivery(h, "1613 Wadsworth Way")

	h.drain()
	if push := onlyPush(t, h, id); push.State != StateNotFound || push.Lat != nil || push.Corrected {
		t.Fatalf("push = %+v", push)
	}
	if len(h.db.cache) != 0 {
		t.Errorf("cache = %v", h.db.cache)
	}
}

func TestAreaRetryUsesTheMapBBox(t *testing.T) {
	h := newHarness(t)
	p := newAreaProvider("fake")
	h.useGeocoder(p)
	p.area["1613 wadsworth way, md"] = []Candidate{wadsworth}
	// The published map covers only the area north of the store.
	h.stores.loc.MapBBox = []float64{testStore.Lng - 0.1, testStore.Lat, testStore.Lng + 0.1, testStore.Lat + 0.1}
	id := onlineDelivery(h, "1613 Wadsworth Way")
	h.drain()
	if push := onlyPush(t, h, id); push.State != StateNotFound {
		t.Fatalf("outside the map bbox: push = %+v", push)
	}

	h.stores.loc.MapBBox = []float64{-76.7, 39.3, -76.5, 39.5}
	id2 := onlineDelivery(h, "1613 Wadsworth Way, HOUSE")
	h.drain()
	if push := onlyPush(t, h, id2); push.State != StateLocated || !push.Corrected {
		t.Fatalf("inside the map bbox: push = %+v", push)
	}
}

func TestAreaRetrySeveralIsCheck(t *testing.T) {
	h := newHarness(t)
	p := newAreaProvider("fake")
	h.useGeocoder(p)
	other := cand("1613 Wadsworth Way", "Parkville", "21234", 39.3900, -76.5400) // a second house, ~4 km away
	p.area["1613 wadsworth way, md"] = []Candidate{wadsworth, other}
	id := onlineDelivery(h, "1613 Wadsworth Way")

	h.drain()
	push := onlyPush(t, h, id)
	if push.State != StateCheck || len(push.Candidates) != 2 || push.Lat != nil || push.Corrected {
		t.Fatalf("push = %+v", push)
	}
	if push.Candidates[0].Label != wadsworth.Label || push.Candidates[1].Label != other.Label {
		t.Errorf("candidates = %+v", push.Candidates)
	}
	if len(h.db.cache) != 0 {
		t.Error("a check is never cached")
	}
	// The usual pick finishes it.
	h.rpc(h.svc.HandleChoose, map[string]any{"order_id": id.String(), "index": 1})
	h.drain()
	if p := onlyPush(t, h, id); p.State != StateLocated || p.Quality != QualityPinned || !p.Confirmed || p.Corrected {
		t.Errorf("chosen push = %+v", p)
	}
}

func TestAreaRetryHouseMatchBeatsRoadOnly(t *testing.T) {
	h := newHarness(t)
	p := newAreaProvider("fake")
	h.useGeocoder(p)
	road := newCandidate("Wadsworth Way", "Baltimore", "MD", "21239", Point{Lat: 39.3700, Lng: -76.5700}, QualityApprox)
	p.area["1613 wadsworth way, md"] = []Candidate{road, wadsworth}
	id := onlineDelivery(h, "1613 Wadsworth Way")

	h.drain()
	if push := onlyPush(t, h, id); push.State != StateLocated || *push.Lat != wadsworth.Lat || !push.Corrected {
		t.Fatalf("push = %+v", push)
	}
}

func TestAreaRetryNoneIsNotFound(t *testing.T) {
	h := newHarness(t)
	p := newAreaProvider("fake")
	h.useGeocoder(p)
	id := onlineDelivery(h, "1613 Wadsworth Way, HOUSE")
	h.drain()
	if push := onlyPush(t, h, id); push.State != StateNotFound {
		t.Fatalf("push = %+v", push)
	}
	if got := p.takeCalls(); len(got) != 3 {
		t.Errorf("calls = %v (normal, cleaned, area)", got)
	}

	// No house number: no area guess.
	id2 := onlineDelivery(h, "Towson University")
	h.drain()
	if push := onlyPush(t, h, id2); push.State != StateNotFound {
		t.Fatalf("push = %+v", push)
	}
	if got := p.takeCalls(); len(got) != 1 {
		t.Errorf("calls = %v (normal only)", got)
	}
}

func TestAreaRetryProviderFailureRetries(t *testing.T) {
	h := newHarness(t)
	p := newAreaProvider("fake")
	p.fail = errors.New("nominatim: http 503")
	h.useGeocoder(p)
	id := onlineDelivery(h, "1613 Wadsworth Way")
	h.drain()
	if ps := h.agent.take(); len(ps) != 0 {
		t.Fatalf("a provider failure must not be final: %+v", ps)
	}
	g := h.db.orderGeo[id]
	if g.State != StatePending || g.LastError == "" || !g.NextTryAt.Time.After(h.clock) {
		t.Fatalf("row = %+v", g)
	}
	p.fail = nil
	p.area["1613 wadsworth way, md"] = []Candidate{wadsworth}
	h.clock = h.clock.Add(time.Minute)
	h.drain()
	if push := onlyPush(t, h, id); push.State != StateLocated || !push.Corrected {
		t.Fatalf("push = %+v", push)
	}
}

func TestAreaRetryExternalStop(t *testing.T) {
	h := newHarness(t)
	p := newAreaProvider("fake")
	h.useGeocoder(p)
	p.area["5415 york rd, md"] = []Candidate{yorkRd}
	stop := uuid.New()
	if got := h.rpc(h.svc.HandleLocate, locateBody(stop, "5415 YORK RD, AROMA PIZZA", "21286")); got["ok"] != true {
		t.Fatalf("reply = %v", got)
	}
	h.drain()
	push := onlyPush(t, h, stop)
	if push.State != StateLocated || *push.Lat != yorkRd.Lat || !push.Corrected {
		t.Fatalf("push = %+v", push)
	}
	if st := h.db.stops[stop]; !st.Corrected {
		t.Errorf("stop = %+v", st)
	}
	key := NormalizeKey(Address{Line1: "5415 YORK RD, AROMA PIZZA", City: "Towson", State: "MD", Zip: "21286"})
	if _, ok := h.db.cache[AreaCacheKey(h.scope.LocationID, key)]; !ok {
		t.Error("stop's corrected result not cached for its input")
	}
	// The store moved: the stop keeps its own point, still corrected.
	h.svc.StoreMoved(context.Background(), h.scope.TenantID, h.scope.LocationID)
	h.drain()
	if p := onlyPush(t, h, stop); !p.Corrected || *p.Lat != yorkRd.Lat {
		t.Errorf("re-routed stop = %+v", p)
	}
	// A pin is staff's own point: not corrected.
	h.rpc(h.svc.HandlePin, map[string]any{"order_id": stop.String(), "lat": 39.356, "lng": -76.61})
	h.drain()
	if p := onlyPush(t, h, stop); p.Corrected || p.Quality != QualityPinned {
		t.Errorf("pinned stop = %+v", p)
	}
}

// --- geo.search

func searchBody(id uuid.UUID, q string) map[string]any {
	return map[string]any{"order_id": id.String(), "query": q}
}

func TestSearchOnlineOrderMergesProvidersNearTheStore(t *testing.T) {
	h := newHarness(t)
	census, nomi := newAreaProvider("census"), newAreaProvider("nominatim")
	h.useGeocoder(Chain{census, nomi})
	nomi.full["1 main st, towson, md 21286"] = []Candidate{cand("1 Main St", "Towson", "21286", 39.40, -76.60)}
	id := onlineDelivery(h, "1 Main St")
	h.drain()
	if p := onlyPush(t, h, id); p.State != StateLocated {
		t.Fatalf("first locate = %+v", p)
	}

	c := func(n int) Candidate {
		return cand(fmt.Sprintf("%d Wadsworth Way", 1600+n), "Baltimore", "21239", 39.36+float64(n)*0.002, -76.58)
	}
	census.area["1613 wadsworth way, md"] = []Candidate{wadsworth, c(1)}
	nearDup := wadsworth
	nearDup.Lat += 0.0003 // the same house from the other provider, ~30 m off
	nomi.area["1613 wadsworth way, md"] = []Candidate{nearDup, c(2), farWadsworth, c(3), c(4), c(5)}

	if got := h.rpc(h.svc.HandleSearch, searchBody(id, "  1613   Wadsworth Way ")); got["ok"] != true {
		t.Fatalf("reply = %v", got)
	}
	h.drain()
	push := onlyPush(t, h, id)
	if push.State != StateCheck || push.Lat != nil || push.Note != "" {
		t.Fatalf("push = %+v", push)
	}
	var labels []string
	for _, c := range push.Candidates {
		labels = append(labels, c.Line1)
	}
	want := []string{"1613 Wadsworth Way", "1601 Wadsworth Way", "1602 Wadsworth Way", "1603 Wadsworth Way", "1604 Wadsworth Way"}
	if fmt.Sprint(labels) != fmt.Sprint(want) {
		t.Errorf("candidates = %v, want %v (merged, repeat + far one dropped, max %d)", labels, want, MaxSearchResults)
	}
	for _, p := range []*areaProvider{census, nomi} {
		calls := p.takeCalls()
		if len(calls) == 0 || calls[len(calls)-1] != "area:1613 wadsworth way, md" {
			t.Errorf("%s calls = %v", p.name, calls)
		}
	}
	if g := h.db.orderGeo[id]; g.State != StateCheck || !g.PushedAt.Valid {
		t.Errorf("row = %+v", g)
	}

	// Staff pick one: located, pinned for the order's own address.
	h.rpc(h.svc.HandleChoose, map[string]any{"order_id": id.String(), "index": 0})
	h.drain()
	p := onlyPush(t, h, id)
	if p.State != StateLocated || p.Quality != QualityPinned || !p.Confirmed || p.ConfirmedAddress == nil || p.ConfirmedAddress.Line1 != "1613 Wadsworth Way" {
		t.Errorf("chosen = %+v", p)
	}
	if c := h.db.cache[keyOf("1 Main St")]; !c.Pinned || c.Lat != wadsworth.Lat {
		t.Errorf("pin cache = %+v", c)
	}
}

func TestSearchNoMatchAndLimits(t *testing.T) {
	h := newHarness(t)
	p := newAreaProvider("fake")
	h.useGeocoder(p)
	p.area["9 nowhere ln, md"] = []Candidate{farWadsworth} // outside the area only
	id := onlineDelivery(h, "9 Nowhere Ln")
	h.drain()
	onlyPush(t, h, id) // not_found from the locate

	if got := h.rpc(h.svc.HandleSearch, searchBody(id, "9 Nowhere Ln")); got["ok"] != true {
		t.Fatalf("reply = %v", got)
	}
	h.drain()
	push := onlyPush(t, h, id)
	if push.State != StateNotFound || push.Note != NoteSearchNoMatch || push.Candidates != nil {
		t.Fatalf("push = %+v", push)
	}
	raw, _ := json.Marshal(push)
	if !strings.Contains(string(raw), `"note":"no match for that search"`) {
		t.Errorf("wire: %s", raw)
	}

	// One search per order per 3 s.
	got := h.rpc(h.svc.HandleSearch, searchBody(id, "9 Nowhere Lane"))
	if got["ok"] != false || got["code"] != "too_soon" {
		t.Fatalf("second search: %v", got)
	}
	other := onlineDelivery(h, "2 Other St")
	if got := h.rpc(h.svc.HandleSearch, searchBody(other, "2 Other St")); got["ok"] != true {
		t.Errorf("another order is not limited: %v", got)
	}
	h.clock = h.clock.Add(SearchGap)
	if got := h.rpc(h.svc.HandleSearch, searchBody(id, "9 Nowhere Lane")); got["ok"] != true {
		t.Errorf("after the gap: %v", got)
	}

	// Unknown order; bad payloads are 400s.
	if got := h.rpc(h.svc.HandleSearch, searchBody(uuid.New(), "1 Main St")); got["ok"] != false || got["code"] != "not_found" {
		t.Errorf("unknown order: %v", got)
	}
	for _, bad := range []string{
		`{"order_id":"nope","query":"1 Main St"}`,
		`{"order_id":"` + id.String() + `","query":"   "}`,
		`{"order_id":"` + id.String() + `"}`,
		`{"order_id":"` + id.String() + `","query":"` + strings.Repeat("a", MaxSearchQuery+1) + `"}`,
		`[]`,
	} {
		if _, err := h.svc.HandleSearch(context.Background(), h.scope, json.RawMessage(bad)); !errors.Is(err, rpc.ErrBadPayload) {
			t.Errorf("%.60s: err = %v", bad, err)
		}
	}
	if _, err := h.svc.HandleSearch(context.Background(), h.scope, json.RawMessage(
		`{"order_id":"`+other.String()+`","query":"`+strings.Repeat("é", MaxSearchQuery)+`"}`)); err != nil {
		t.Errorf("200 characters is fine: %v", err)
	}
}

func TestSearchProviderFailureSaysTryAgain(t *testing.T) {
	h := newHarness(t)
	p := newAreaProvider("fake")
	h.useGeocoder(p)
	id := onlineDelivery(h, "1 Main St")
	h.drain()
	h.agent.take()
	p.fail = errors.New("http 503")
	h.rpc(h.svc.HandleSearch, searchBody(id, "1613 Wadsworth Way"))
	h.drain()
	if push := onlyPush(t, h, id); push.State != StateNotFound || push.Note != NoteSearchFailed {
		t.Fatalf("push = %+v", push)
	}
}

func TestSearchExternalStop(t *testing.T) {
	h := newHarness(t)
	p := newAreaProvider("fake")
	h.useGeocoder(p)
	p.full["100 york rd, towson, md 21204"] = []Candidate{cand("100 York Rd", "Towson", "21204", 39.4010, -76.6010)}
	stop := uuid.New()
	h.rpc(h.svc.HandleLocate, locateBody(stop, "100 York Rd", "21204"))
	h.drain()
	onlyPush(t, h, stop)
	if _, ok := h.db.work[stop]; ok {
		t.Fatal("located stop kept its address text")
	}

	p.area["5415 york rd, md"] = []Candidate{yorkRd}
	if got := h.rpc(h.svc.HandleSearch, searchBody(stop, "5415 York Rd")); got["ok"] != true {
		t.Fatalf("reply = %v", got)
	}
	h.drain()
	push := onlyPush(t, h, stop)
	if push.State != StateCheck || len(push.Candidates) != 1 || push.Candidates[0].Line1 != "5415 York Road" {
		t.Fatalf("push = %+v", push)
	}
	if w := h.db.work[stop]; w == nil || string(w.Address) != "{}" || w.AddressKey != "" {
		t.Fatalf("work row = %+v (candidates only, no address)", w)
	}
	h.rpc(h.svc.HandleChoose, map[string]any{"order_id": stop.String(), "index": 0})
	h.drain()
	if p := onlyPush(t, h, stop); p.State != StateLocated || p.Quality != QualityPinned || !p.Confirmed || *p.Lat != yorkRd.Lat {
		t.Errorf("chosen = %+v", p)
	}

	// A stop main never heard of.
	if got := h.rpc(h.svc.HandleSearch, searchBody(uuid.New(), "5415 York Rd")); got["code"] != "not_found" {
		t.Errorf("unknown stop: %v", got)
	}
}

func TestProvidersGeocodeIn(t *testing.T) {
	ctx := context.Background()
	bayonne := BoxAround(Point{Lat: 40.6666, Lng: -74.1158}, 5000)
	in := Address{Line1: "12 W 33rd St, side door", City: "Towson", State: "NJ", Zip: "21286"}
	in.Line1 = CleanStreet(in.Line1)

	var nomReq atomic.Pointer[http.Request]
	n := &Nominatim{BaseURL: serve(t, 200, "nominatim_hit.json", &nomReq).URL, Contact: "owner@example.com"}
	r, err := n.GeocodeIn(ctx, in, bayonne)
	if err != nil || r.Lat != 40.6689524 || r.Quality != QualityExact {
		t.Fatalf("nominatim: %+v %v", r, err)
	}
	q := nomReq.Load().URL.Query()
	wantBox := fmt.Sprintf("%.5f,%.5f,%.5f,%.5f", bayonne.MinLng, bayonne.MaxLat, bayonne.MaxLng, bayonne.MinLat)
	if q.Get("q") != "12 W 33rd St, NJ" || q.Get("bounded") != "1" || q.Get("viewbox") != wantBox ||
		q.Has("street") || q.Has("city") || q.Has("postalcode") || q.Get("countrycodes") != "us" {
		t.Errorf("nominatim query %v", q)
	}
	if ua := nomReq.Load().Header.Get("User-Agent"); !strings.Contains(ua, "owner@example.com") {
		t.Errorf("UA = %q", ua)
	}

	var censusReq atomic.Pointer[http.Request]
	c := &Census{BaseURL: serve(t, 200, "census_match.json", &censusReq).URL}
	if _, err := c.GeocodeIn(ctx, in, bayonne); err != nil {
		t.Fatalf("census: %v", err)
	}
	if a := censusReq.Load().URL.Query().Get("address"); a != "12 W 33rd St, NJ" {
		t.Errorf("census address = %q (no town, no zip)", a)
	}

	// The Census has no bound: its match outside the box is dropped and the
	// chain goes on to Nominatim; nothing inside → ErrNotFound.
	towson := BoxAround(testStore, AreaHalfSideM)
	if _, err := (Chain{c, &Nominatim{BaseURL: serve(t, 200, "nominatim_hit.json", nil).URL}}).GeocodeIn(ctx, in, towson); !errors.Is(err, ErrNotFound) {
		t.Errorf("out-of-box matches only: %v", err)
	}
	r, err = Chain{c, n}.GeocodeIn(ctx, in, bayonne)
	if err != nil || r.Source != "census" {
		t.Errorf("in-box census match first: %+v %v", r, err)
	}
	// A geocoder without GeocodeIn is asked with a bias and filtered.
	g := &fakeProvider{known: map[string][]Candidate{"12 w 33rd st": {cand("12 W 33rd St", "Bayonne", "07002", 40.6689, -74.1142)}}}
	if _, err := geocodeIn(ctx, g, in, towson); !errors.Is(err, ErrNotFound) {
		t.Errorf("plain geocoder, out of box: %v", err)
	}
	if r, err := geocodeIn(ctx, g, in, bayonne); err != nil || r.Lat != 40.6689 {
		t.Errorf("plain geocoder, in box: %+v %v", r, err)
	}
}
