package geo

import (
	"context"
	"testing"
	"time"

	"quiccpos/main/internal/domain/order"

	"github.com/google/uuid"
)

// counterOrder is a counter order as main holds it, delivering to a (nil = a pickup).
func counterOrder(h *harness, id uuid.UUID, a *order.Address) *order.Order {
	f := order.Fulfillment{Type: order.FulfillmentPickup}
	if a != nil {
		f = order.Fulfillment{Type: order.FulfillmentDelivery, Address: a}
	}
	return order.Rehydrate(order.RehydrateInput{ID: order.ID(id), TenantID: order.TenantID(h.scope.TenantID),
		LocationID: order.LocationID(h.scope.LocationID), Source: order.SourceCounter, SourceEventID: id.String(),
		Status: order.StatusAccepted, Fulfillment: f, Currency: "USD", PlacedAt: h.clock, ReceivedAt: h.clock, UpdatedAt: h.clock})
}

// A sent delivery whose customer gave another address (the Pi's order.changed, TERMINAL-FLOWS CHG-14): main
// locates the new address as at intake and pushes it, so the Pi's map, drive time, pools and AUTO follow.
func TestChangedAddressIsLocatedAgain(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.knows("12 Sample Rd", cand("12 Sample Rd", "Towson", "21204", 39.4011, -76.6019))
	h.knows("500 York Rd", cand("500 York Rd", "Towson", "21204", 39.3990, -76.6040))
	id := uuid.New()
	first := &order.Address{Line1: "12 Sample Rd", City: "Towson", State: "MD", PostalCode: "21204"}
	h.db.addOrder(id, *first)
	h.svc.OnOrderCreated(ctx, counterOrder(h, id, first))
	h.drain()
	if p := onlyPush(t, h, id); p.State != StateLocated || *p.Lat != 39.4011 {
		t.Fatalf("first push = %+v", p)
	}

	// Only the apartment / notes moved: the same place, nothing is worked again.
	same := &order.Address{Line1: "12 sample rd", Line2: "Apt 2", City: "Towson", State: "MD", PostalCode: "21204", Notes: "side door"}
	h.svc.OnOrderMoved(ctx, counterOrder(h, id, first), counterOrder(h, id, same))
	h.drain()
	if ps := h.agent.take(); len(ps) != 0 {
		t.Fatalf("the same address pushed again: %+v", ps)
	}

	// Hours later the customer calls back with another street: located again (the retry window restarts).
	h.clock = h.clock.Add(5 * time.Hour)
	moved := &order.Address{Line1: "500 York Rd", City: "Towson", State: "MD", PostalCode: "21204"}
	h.db.addOrder(id, *moved) // ChangeCounterOrder wrote the new fulfillment
	h.svc.OnOrderMoved(ctx, counterOrder(h, id, first), counterOrder(h, id, moved))
	h.drain()
	p := onlyPush(t, h, id)
	if p.State != StateLocated || *p.Lat != 39.3990 || *p.Lng != -76.6040 || p.StoreToS == nil || p.Confirmed {
		t.Fatalf("push after the move = %+v", p)
	}

	// A takeout moved to a delivery (no geo row yet) is located; a delivery moved back to a pickup is left alone.
	id2 := uuid.New()
	h.db.addOrder(id2, *first)
	h.svc.OnOrderMoved(ctx, counterOrder(h, id2, nil), counterOrder(h, id2, first))
	h.drain()
	if p := onlyPush(t, h, id2); p.State != StateLocated || *p.Lat != 39.4011 {
		t.Fatalf("takeout → delivery push = %+v", p)
	}
	h.svc.OnOrderMoved(ctx, counterOrder(h, id2, first), counterOrder(h, id2, nil))
	h.drain()
	if ps := h.agent.take(); len(ps) != 0 {
		t.Fatalf("a pickup was pushed: %+v", ps)
	}
}

func TestAddressMoved(t *testing.T) {
	h := newHarness(t)
	id := uuid.New()
	a := &order.Address{Line1: "12 Sample Rd", City: "Towson", State: "MD", PostalCode: "21204"}
	cases := []struct {
		name          string
		before, after *order.Order
		want          bool
	}{
		{"unknown before", nil, counterOrder(h, id, a), true},
		{"was a pickup", counterOrder(h, id, nil), counterOrder(h, id, a), true},
		{"same street, other case + notes", counterOrder(h, id, a), counterOrder(h, id, &order.Address{Line1: "12 SAMPLE RD", City: "towson", State: "MD", PostalCode: "21204", Notes: "x"}), false},
		{"another street", counterOrder(h, id, a), counterOrder(h, id, &order.Address{Line1: "14 Sample Rd", City: "Towson", State: "MD", PostalCode: "21204"}), true},
		{"another town", counterOrder(h, id, a), counterOrder(h, id, &order.Address{Line1: "12 Sample Rd", City: "Parkville", State: "MD", PostalCode: "21234"}), true},
	}
	for _, c := range cases {
		if got := AddressMoved(c.before, c.after); got != c.want {
			t.Errorf("%s: AddressMoved = %v, want %v", c.name, got, c.want)
		}
	}
}
