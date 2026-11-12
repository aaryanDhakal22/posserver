package geo

import (
	"context"
	"encoding/json"
	"testing"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
)

func onlyPush(t *testing.T, h *harness, id uuid.UUID) Push {
	t.Helper()
	ps := h.agent.take()
	if len(ps) != 1 || ps[0].OrderID != id.String() {
		t.Fatalf("pushes = %+v, want one for %s", ps, id)
	}
	return ps[0]
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

func insertParams(h *harness, id uuid.UUID) models.InsertOrderGeoParams {
	return models.InsertOrderGeoParams{OrderID: pgUUID(id), TenantID: pgUUID(h.scope.TenantID), LocationID: pgUUID(h.scope.LocationID)}
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

}

// geo.choose / geo.pin / geo.search for an id main has no order for (since H3 there are
// no external stops) answer not_found.
func TestChooseAndPinUnknownOrder(t *testing.T) {
	h := newHarness(t)
	id := uuid.New().String()
	if got := h.rpc(h.svc.HandleChoose, map[string]any{"order_id": id, "index": 0}); got["ok"] != false || got["code"] != "not_found" {
		t.Errorf("choose = %v", got)
	}
	if got := h.rpc(h.svc.HandlePin, map[string]any{"order_id": id, "lat": 39.39, "lng": -76.59}); got["ok"] != false || got["code"] != "not_found" {
		t.Errorf("pin = %v", got)
	}
	if got := h.rpc(h.svc.HandleSearch, searchBody(uuid.MustParse(id), "5415 York Rd")); got["code"] != "not_found" {
		t.Errorf("search = %v", got)
	}
}
