package geo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/models"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
)

func locateBody(id uuid.UUID, line1, zip string) map[string]any {
	return map[string]any{"order_id": id.String(), "address": map[string]any{
		"line1": line1, "line2": "Apt 2", "city": "Towson", "state": "MD", "postal_code": zip}}
}

func onlyPush(t *testing.T, h *harness, id uuid.UUID) Push {
	t.Helper()
	ps := h.agent.take()
	if len(ps) != 1 || ps[0].OrderID != id.String() {
		t.Fatalf("pushes = %+v, want one for %s", ps, id)
	}
	return ps[0]
}

func TestLocateExternalStopWithoutAnOrdersRow(t *testing.T) {
	h := newHarness(t)
	h.knows("100 York Rd", cand("100 York Rd", "Towson", "21204", 39.4010, -76.6010))
	stop := uuid.New() // the agent's own id: main has no orders row for it

	if got := h.rpc(h.svc.HandleLocate, locateBody(stop, "100 York Rd", "21204")); got["ok"] != true {
		t.Fatalf("reply = %v", got)
	}
	if len(h.db.orders) != 0 || len(h.db.orderGeo) != 0 {
		t.Fatal("geo.locate must not create an order")
	}
	w := h.db.work[stop]
	if w == nil || w.AddressKey != NormalizeKey(Address{Line1: "100 York Rd", City: "Towson", State: "MD", Zip: "21204"}) {
		t.Fatalf("work row = %+v", w)
	}
	if exp := h.db.stops[stop].ExpiresAt.Time; !exp.Equal(h.clock.Add(24 * time.Hour)) {
		t.Errorf("expires_at = %v", exp)
	}

	h.drain()
	p := onlyPush(t, h, stop)
	if p.State != StateLocated || p.Quality != QualityExact || *p.Lat != 39.4010 || *p.Lng != -76.6010 ||
		p.Polyline != "fake" || p.DistanceM == 0 || p.StoreToS == nil || p.ToStoreS == nil {
		t.Errorf("push = %+v", p)
	}
	if p.Candidates != nil || p.ConfirmedAddress != nil {
		t.Errorf("a confident result has no candidates/confirmed address: %+v", p)
	}
	st := h.db.stops[stop]
	if st.State != StateLocated || !st.Lat.Valid || !st.PushedAt.Valid {
		t.Errorf("stop = %+v", st)
	}
	// Located: the address text is gone; the point stays.
	if _, ok := h.db.work[stop]; ok {
		t.Error("address text kept after locating")
	}
	if _, ok := h.db.cache[w.AddressKey]; !ok {
		t.Error("geocode not cached")
	}
	h.drain()
	if ps := h.agent.take(); len(ps) != 0 {
		t.Errorf("acked stop pushed again: %+v", ps)
	}

	// Bad payloads are 400s.
	for _, bad := range []string{`{"order_id":"nope","address":{}}`, `{"order_id":"` + stop.String() + `"}`, `[]`} {
		if _, err := h.svc.HandleLocate(context.Background(), h.scope, json.RawMessage(bad)); !errors.Is(err, rpc.ErrBadPayload) {
			t.Errorf("%s: err = %v", bad, err)
		}
	}
}

func TestDriveTimesMixOnlineOrdersAndExternalStops(t *testing.T) {
	h := newHarness(t)
	h.knows("1 Online St", cand("1 Online St", "Towson", "21286", 39.3900, -76.5800))
	h.knows("2 Pos Ave", cand("2 Pos Ave", "Towson", "21286", 39.4100, -76.6000))

	online := uuid.New()
	h.db.addOrder(online, order.Address{Line1: "1 Online St", City: "Towson", State: "MD", PostalCode: "21286"})
	h.svc.q.InsertOrderGeo(context.Background(), insertParams(h, online))
	h.drain()
	onlyPush(t, h, online)

	stop := uuid.New()
	h.rpc(h.svc.HandleLocate, locateBody(stop, "2 Pos Ave", "21286"))
	h.drain()
	p := onlyPush(t, h, stop)
	if len(p.Times) != 1 || p.Times[0].Other != online.String() || p.Times[0].ToS <= 0 || p.Times[0].FromS <= 0 {
		t.Fatalf("stop's times = %+v", p.Times)
	}

	// An online order located now gets the stop in its table too.
	h.knows("3 Online Ct", cand("3 Online Ct", "Towson", "21286", 39.3950, -76.5900))
	o2 := uuid.New()
	h.db.addOrder(o2, order.Address{Line1: "3 Online Ct", City: "Towson", State: "MD", PostalCode: "21286"})
	h.svc.q.InsertOrderGeo(context.Background(), insertParams(h, o2))
	h.drain()
	p = onlyPush(t, h, o2)
	others := map[string]bool{}
	for _, tp := range p.Times {
		others[tp.Other] = true
	}
	if len(p.Times) != 2 || !others[online.String()] || !others[stop.String()] {
		t.Fatalf("online order's times = %+v", p.Times)
	}
}

func TestExternalStopCheckThenChoose(t *testing.T) {
	h := newHarness(t)
	h.knows("931 Taylor Ave",
		cand("931 E Taylor Ave", "Towson", "21286", 39.3982, -76.5895),
		cand("931 W Taylor Ave", "Towson", "21204", 39.4021, -76.6181),
		cand("931 Taylor Rd", "Parkville", "21234", 39.3800, -76.5400),
		cand("931 Tailor Ln", "Essex", "21221", 39.3100, -76.4700))
	stop := uuid.New()
	h.rpc(h.svc.HandleLocate, locateBody(stop, "931 Taylor Ave", "")) // no zip: E and W both agree
	h.drain()
	p := onlyPush(t, h, stop)
	if p.State != StateCheck || p.Lat != nil || p.Lng != nil || p.Quality != "" || len(p.Candidates) != 3 {
		t.Fatalf("check push = %+v", p)
	}
	if c := p.Candidates[1]; c.Label != "931 W Taylor Ave, Towson, MD 21204" || c.Lat != 39.4021 || c.PostalCode != "21204" {
		t.Errorf("candidate 1 = %+v", c)
	}
	raw, _ := json.Marshal(p)
	var wire map[string]any
	_ = json.Unmarshal(raw, &wire)
	c0 := wire["candidates"].([]any)[0].(map[string]any)
	for _, k := range []string{"label", "line1", "city", "state", "postal_code", "lat", "lng"} {
		if _, ok := c0[k]; !ok {
			t.Errorf("candidate wire shape lacks %s: %v", k, c0)
		}
	}
	if _, ok := c0["quality"]; ok || len(c0) != 7 {
		t.Errorf("candidate wire shape = %v", c0)
	}
	key := h.db.work[stop].AddressKey
	if _, ok := h.db.cache[key]; ok {
		t.Error("an ambiguous result must not be cached")
	}
	if h.db.work[stop] == nil || len(decodeCandidates(h.db.work[stop].Candidates)) != 3 {
		t.Fatal("candidates must be kept for choose")
	}

	if got := h.rpc(h.svc.HandleChoose, map[string]any{"order_id": stop.String(), "index": 3}); got["ok"] != false || got["code"] != "bad_index" {
		t.Errorf("bad index reply = %v", got)
	}
	if got := h.rpc(h.svc.HandleChoose, map[string]any{"order_id": stop.String(), "index": 1}); got["ok"] != true {
		t.Fatalf("choose reply = %v", got)
	}
	if c := h.db.cache[key]; !c.Pinned || c.Lat != 39.4021 {
		t.Errorf("cache after choose = %+v", c)
	}
	h.drain()
	p = onlyPush(t, h, stop)
	want := ConfirmedAddress{Line1: "931 W Taylor Ave", City: "Towson", State: "MD", PostalCode: "21204"}
	if p.State != StateLocated || p.Quality != QualityPinned || *p.Lat != 39.4021 || p.ConfirmedAddress == nil || *p.ConfirmedAddress != want || p.Candidates != nil {
		t.Fatalf("chosen push = %+v (confirmed %+v)", p, p.ConfirmedAddress)
	}
	// Acked: the confirmed address (text) is gone now.
	if _, ok := h.db.work[stop]; ok {
		t.Error("address text kept after the confirmed address was acked")
	}

	// The same address again later: the pinned cache answers, no geocode.
	n := h.prov.geocode
	again := uuid.New()
	h.rpc(h.svc.HandleLocate, locateBody(again, "931 Taylor Avenue", ""))
	h.drain()
	if p := onlyPush(t, h, again); p.State != StateLocated || p.Quality != QualityPinned || *p.Lat != 39.4021 || h.prov.geocode != n {
		t.Errorf("re-ordered address: %+v (geocodes %d→%d)", p, n, h.prov.geocode)
	}

	// No candidates any more / unknown id.
	if got := h.rpc(h.svc.HandleChoose, map[string]any{"order_id": stop.String(), "index": 0}); got["code"] != "no_candidates" {
		t.Errorf("choose without candidates = %v", got)
	}
	if got := h.rpc(h.svc.HandleChoose, map[string]any{"order_id": uuid.NewString(), "index": 0}); got["code"] != "not_found" {
		t.Errorf("choose unknown = %v", got)
	}
	if _, err := h.svc.HandleChoose(context.Background(), h.scope, json.RawMessage(`{"order_id":"`+stop.String()+`"}`)); !errors.Is(err, rpc.ErrBadPayload) {
		t.Errorf("missing index err = %v", err)
	}
}

func TestOnlineOrderCheckThenChoose(t *testing.T) {
	h := newHarness(t)
	h.knows("100 Main St",
		cand("100 Main St", "Annapolis", "21401", 38.9774, -76.4878),
		cand("100 Main St", "Laurel", "20707", 39.1094, -76.8572))
	id := uuid.New()
	h.db.addOrder(id, order.Address{Line1: "100 Main St", City: "", State: "MD"})
	h.svc.q.InsertOrderGeo(context.Background(), insertParams(h, id))
	h.drain()
	p := onlyPush(t, h, id)
	if p.State != StateCheck || len(p.Candidates) != 2 || p.Lat != nil {
		t.Fatalf("check push = %+v", p)
	}
	if got := h.rpc(h.svc.HandleChoose, map[string]any{"order_id": id.String(), "index": 0}); got["ok"] != true {
		t.Fatalf("choose = %v", got)
	}
	h.drain()
	p = onlyPush(t, h, id)
	want := ConfirmedAddress{Line1: "100 Main St", City: "Annapolis", State: "MD", PostalCode: "21401"}
	if p.State != StateLocated || p.Quality != QualityPinned || *p.Lat != 38.9774 || p.ConfirmedAddress == nil || *p.ConfirmedAddress != want {
		t.Fatalf("chosen push = %+v", p)
	}
	key := NormalizeKey(Address{Line1: "100 Main St", State: "MD"})
	if c := h.db.cache[key]; !c.Pinned || c.Lat != 38.9774 {
		t.Errorf("cache = %+v", c)
	}
	if g := h.db.orderGeo[id]; g.Candidates != nil || decodeConfirmed(g.ConfirmedAddress) == nil {
		t.Errorf("order_geo after choose = %+v", g)
	}
}

func TestForgetDropsTheStop(t *testing.T) {
	h := newHarness(t)
	stop := uuid.New()
	h.rpc(h.svc.HandleLocate, locateBody(stop, "5 Unknown Way", "21286"))
	if got := h.rpc(h.svc.HandleForget, map[string]any{"order_id": stop.String()}); got["ok"] != true {
		t.Fatalf("forget = %v", got)
	}
	if len(h.db.stops)+len(h.db.work) != 0 {
		t.Fatal("stop / address kept after forget")
	}
	h.drain()
	if ps := h.agent.take(); len(ps) != 0 {
		t.Errorf("forgotten stop pushed: %+v", ps)
	}
	// Idempotent; another scope's stop is untouched.
	if got := h.rpc(h.svc.HandleForget, map[string]any{"order_id": stop.String()}); got["ok"] != true {
		t.Errorf("second forget = %v", got)
	}
	if _, err := h.svc.HandleForget(context.Background(), h.scope, json.RawMessage(`{"order_id":1}`)); !errors.Is(err, rpc.ErrBadPayload) {
		t.Errorf("bad forget err = %v", err)
	}
}

func TestExternalStopExpiresAndAddressIsDiscarded(t *testing.T) {
	h := newHarness(t)
	h.knows("931 Taylor Ave", cand("931 E Taylor Ave", "Towson", "21286", 39.3982, -76.5895), cand("931 W Taylor Ave", "Towson", "21204", 39.4021, -76.6181))
	h.knows("1 Located Rd", cand("1 Located Rd", "Towson", "21286", 39.40, -76.59))
	check, located := uuid.New(), uuid.New()
	h.rpc(h.svc.HandleLocate, locateBody(check, "931 Taylor Ave", ""))
	h.rpc(h.svc.HandleLocate, locateBody(located, "1 Located Rd", "21286"))
	h.drain()
	h.agent.take()
	if h.db.work[check] == nil || h.db.work[located] != nil {
		t.Fatal("only the check stop keeps its address")
	}

	// 6 h: the unpicked address text is discarded; the stops stay.
	h.clock = h.clock.Add(6*time.Hour + time.Second)
	h.svc.sweep(context.Background(), h.clock)
	if len(h.db.work) != 0 || len(h.db.stops) != 2 {
		t.Fatalf("after 6 h: work=%d stops=%d", len(h.db.work), len(h.db.stops))
	}
	if got := h.rpc(h.svc.HandleChoose, map[string]any{"order_id": check.String(), "index": 0}); got["code"] != "no_candidates" {
		t.Errorf("choose after discard = %v", got)
	}

	// 24 h: the stops are gone (and out of every drive-time table).
	h.clock = h.clock.Add(18 * time.Hour)
	if rows, _ := h.db.ListLiveExternalStops(context.Background(), listLive(h)); len(rows) != 0 {
		t.Errorf("expired stop still in the table: %+v", rows)
	}
	h.svc.sweep(context.Background(), h.clock)
	if len(h.db.stops) != 0 {
		t.Fatalf("stops after 24 h = %d", len(h.db.stops))
	}
}

func TestPinAnExternalStop(t *testing.T) {
	h := newHarness(t)
	stop := uuid.New()
	h.rpc(h.svc.HandleLocate, locateBody(stop, "77 Nowhere Ln", "21286"))
	h.drain()
	if p := onlyPush(t, h, stop); p.State != StateNotFound || p.Lat != nil {
		t.Fatalf("push = %+v", p)
	}
	if _, ok := h.db.work[stop]; ok {
		t.Error("address kept after giving up")
	}
	if got := h.rpc(h.svc.HandlePin, map[string]any{"order_id": stop.String(), "lat": 39.39, "lng": -76.58}); got["ok"] != true {
		t.Fatalf("pin = %v", got)
	}
	h.drain()
	if p := onlyPush(t, h, stop); p.State != StateLocated || p.Quality != QualityPinned || *p.Lat != 39.39 || p.ConfirmedAddress != nil {
		t.Fatalf("pinned push = %+v", p)
	}
	if got := h.rpc(h.svc.HandlePin, map[string]any{"order_id": uuid.NewString(), "lat": 39.39, "lng": -76.58}); got["code"] != "not_found" {
		t.Errorf("pin unknown = %v", got)
	}
}

func TestUnackedStopIsRepushedOnReconnect(t *testing.T) {
	h := newHarness(t)
	h.knows("9 Late St", cand("9 Late St", "Towson", "21286", 39.40, -76.59))
	stop := uuid.New()
	h.agent.offline = true
	h.rpc(h.svc.HandleLocate, locateBody(stop, "9 Late St", "21286"))
	h.drain()
	if st := h.db.stops[stop]; st.State != StateLocated || st.PushedAt.Valid || !st.NextTryAt.Time.After(h.clock) {
		t.Fatalf("offline: %+v", st)
	}
	h.agent.offline = false
	h.svc.ConnectDelay = 0
	h.svc.WakeLocation(h.scope)
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.db.mu.Lock()
		w := h.db.wakes
		h.db.mu.Unlock()
		if w > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.drain()
	if p := onlyPush(t, h, stop); p.State != StateLocated {
		t.Errorf("re-push = %+v", p)
	}
}

func insertParams(h *harness, id uuid.UUID) models.InsertOrderGeoParams {
	return models.InsertOrderGeoParams{OrderID: pgUUID(id), TenantID: pgUUID(h.scope.TenantID), LocationID: pgUUID(h.scope.LocationID)}
}

func listLive(h *harness) models.ListLiveExternalStopsParams {
	return models.ListLiveExternalStopsParams{TenantID: pgUUID(h.scope.TenantID), LocationID: pgUUID(h.scope.LocationID), MaxRows: 40}
}

// A normal online order whose address the geocoder returns twice (~20 m
// apart) is located, as before 2.1.0, not sent to check.
func TestOnlineOrderDuplicateMatchIsLocated(t *testing.T) {
	h := newHarness(t)
	h.knows("931 Taylor Ave",
		cand("931 Taylor Ave", "Towson", "21286", 39.3982, -76.5895),
		cand("931 Taylor Avenue", "Towson", "21286", 39.39838, -76.5895))
	for _, zip := range []string{"21286", ""} {
		id := uuid.New()
		h.db.addOrder(id, order.Address{Line1: "931 Taylor Ave", City: "Towson", State: "MD", PostalCode: zip})
		h.svc.q.InsertOrderGeo(context.Background(), insertParams(h, id))
		h.drain()
		p := onlyPush(t, h, id)
		if p.State != StateLocated || p.Lat == nil || *p.Lat != 39.3982 || p.Candidates != nil || p.Confirmed {
			t.Fatalf("zip %q: push = %+v", zip, p)
		}
	}
}

// geo.locate with a new address while the worker finishes the old one: the
// worker's save + work-row follow-up must not drop (or overwrite) the new
// work row.
func TestExternalStopWorkRaceKeepsTheNewAddress(t *testing.T) {
	for _, first := range []string{"located", "check"} {
		t.Run(first, func(t *testing.T) {
			h := newHarness(t)
			h.knows("1 Old Rd", cand("1 Old Rd", "Towson", "21286", 39.3900, -76.5800))
			h.knows("931 Taylor Ave", cand("931 E Taylor Ave", "Towson", "21286", 39.3982, -76.5895),
				cand("931 W Taylor Ave", "Towson", "21204", 39.4021, -76.6181))
			h.knows("2 New Rd", cand("2 New Rd", "Towson", "21286", 39.4100, -76.6000))
			stop := uuid.New()
			old := "1 Old Rd"
			if first == "check" {
				old = "931 Taylor Ave" // no zip: ambiguous
			}
			h.rpc(h.svc.HandleLocate, locateBody(stop, old, ""))
			h.db.afterStopSave = func() { h.rpc(h.svc.HandleLocate, locateBody(stop, "2 New Rd", "21286")) }
			// One worker round: it saves the old result, the new locate lands.
			rows, _ := h.db.ListDueExternalStops(context.Background(), models.ListDueExternalStopsParams{Now: ts(h.clock), MaxRows: 50})
			if len(rows) != 1 {
				t.Fatalf("due = %+v", rows)
			}
			h.svc.processStop(context.Background(), rows[0])
			w := h.db.work[stop]
			if w == nil || !strings.Contains(string(w.Address), "2 New Rd") {
				t.Fatalf("the new address was dropped: %+v", w)
			}
			if w.Candidates != nil {
				t.Fatalf("the new work row got the old run's candidates: %s", w.Candidates)
			}
			if st := h.db.stops[stop]; st.State != StatePending {
				t.Fatalf("stop = %+v, want pending again", st)
			}
			h.drain()
			p := h.agent.take()
			if len(p) != 1 || p[0].State != StateLocated || *p[0].Lat != 39.4100 {
				t.Fatalf("pushes = %+v, want the new address located", p)
			}
			if _, ok := h.db.work[stop]; ok {
				t.Error("the new address must be dropped once it is located")
			}
		})
	}
}

// "confirmed": true rides only the push that comes straight from geo.pin /
// geo.choose — never a first locate, a pinned cache hit or a later re-route.
func TestConfirmedOnlyOnPinAndChoosePushes(t *testing.T) {
	wire := func(p Push) map[string]any {
		raw, _ := json.Marshal(p)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return m
	}
	ctx := context.Background()

	t.Run("online pin", func(t *testing.T) {
		h := newHarness(t)
		h.knows("5 Oak St", cand("5 Oak St", "Towson", "21286", 39.3950, -76.5850))
		id := uuid.New()
		h.db.addOrder(id, order.Address{Line1: "5 Oak St", City: "Towson", State: "MD", PostalCode: "21286"})
		h.svc.q.InsertOrderGeo(ctx, insertParams(h, id))
		h.drain()
		if p := onlyPush(t, h, id); p.Confirmed {
			t.Fatalf("first locate: %+v", p)
		} else if _, ok := wire(p)["confirmed"]; ok {
			t.Errorf("confirmed must be absent on the wire: %v", wire(p))
		}
		h.rpc(h.svc.HandlePin, map[string]any{"order_id": id.String(), "lat": 39.3960, "lng": -76.5860})
		h.drain()
		p := onlyPush(t, h, id)
		if !p.Confirmed || p.Quality != QualityPinned || wire(p)["confirmed"] != true {
			t.Fatalf("pin push = %+v", p)
		}
		// The store moved: re-routed, not re-confirmed.
		h.svc.StoreMoved(ctx, h.scope.TenantID, h.scope.LocationID)
		h.drain()
		if p := onlyPush(t, h, id); p.Confirmed || p.Quality != QualityPinned {
			t.Errorf("re-route push = %+v", p)
		}
		// Another order at the pinned address: a cache hit, not a confirmation.
		o2 := uuid.New()
		h.db.addOrder(o2, order.Address{Line1: "5 Oak St", City: "Towson", State: "MD", PostalCode: "21286"})
		h.svc.q.InsertOrderGeo(ctx, insertParams(h, o2))
		h.drain()
		if p := onlyPush(t, h, o2); p.Confirmed || p.Quality != QualityPinned {
			t.Errorf("pinned cache hit = %+v", p)
		}
	})

	t.Run("online choose, offline then reconnect", func(t *testing.T) {
		h := newHarness(t)
		h.knows("100 Main St", cand("100 Main St", "Annapolis", "21401", 38.9774, -76.4878),
			cand("100 Main St", "Laurel", "20707", 39.1094, -76.8572))
		id := uuid.New()
		h.db.addOrder(id, order.Address{Line1: "100 Main St", State: "MD"})
		h.svc.q.InsertOrderGeo(ctx, insertParams(h, id))
		h.drain()
		if p := onlyPush(t, h, id); p.State != StateCheck || p.Confirmed {
			t.Fatalf("check push = %+v", p)
		}
		h.rpc(h.svc.HandleChoose, map[string]any{"order_id": id.String(), "index": 1})
		h.agent.offline = true
		h.drain()
		h.agent.offline = false
		h.db.orderGeo[id].NextTryAt = ts(h.clock) // the reconnect wake
		h.drain()
		if p := onlyPush(t, h, id); !p.Confirmed || p.ConfirmedAddress == nil || p.ConfirmedAddress.City != "Laurel" {
			t.Fatalf("chosen push (after reconnect) = %+v", p)
		}
		if h.db.orderGeo[id].Confirmed {
			t.Error("an acked push must spend confirmed")
		}
	})

	t.Run("external stop choose + pin", func(t *testing.T) {
		h := newHarness(t)
		h.knows("931 Taylor Ave", cand("931 E Taylor Ave", "Towson", "21286", 39.3982, -76.5895),
			cand("931 W Taylor Ave", "Towson", "21204", 39.4021, -76.6181))
		stop := uuid.New()
		h.rpc(h.svc.HandleLocate, locateBody(stop, "931 Taylor Ave", ""))
		h.drain()
		if p := onlyPush(t, h, stop); p.State != StateCheck || p.Confirmed {
			t.Fatalf("check push = %+v", p)
		}
		h.rpc(h.svc.HandleChoose, map[string]any{"order_id": stop.String(), "index": 0})
		h.drain()
		if p := onlyPush(t, h, stop); !p.Confirmed || wire(p)["confirmed"] != true {
			t.Fatalf("chosen push = %+v", p)
		}
		h.rpc(h.svc.HandlePin, map[string]any{"order_id": stop.String(), "lat": 39.3990, "lng": -76.5900})
		h.drain()
		if p := onlyPush(t, h, stop); !p.Confirmed || *p.Lat != 39.3990 {
			t.Fatalf("pinned push = %+v", p)
		}
		h.svc.StoreMoved(ctx, h.scope.TenantID, h.scope.LocationID)
		h.drain()
		if p := onlyPush(t, h, stop); p.Confirmed {
			t.Errorf("re-route push = %+v", p)
		}
		// The same address again: the pinned cache answers, unconfirmed.
		again := uuid.New()
		h.rpc(h.svc.HandleLocate, locateBody(again, "931 Taylor Ave", ""))
		h.drain()
		if p := onlyPush(t, h, again); p.Confirmed || p.Quality != QualityPinned {
			t.Errorf("pinned cache hit = %+v", p)
		}
	})
}
