package online

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"quiccpos/main/internal/domain/order"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// stubRouter routes by a fixed name → printer map and records what it saw.
type stubRouter struct {
	printers map[string]uuid.UUID
	err      error
	gotLoc   uuid.UUID
	gotNames []string
}

func (r *stubRouter) RouteItems(_ context.Context, loc uuid.UUID, names []string) ([]order.ItemRoute, error) {
	r.gotLoc, r.gotNames = loc, names
	if r.err != nil {
		return nil, r.err
	}
	out := make([]order.ItemRoute, len(names))
	for i, n := range names {
		if pid, ok := r.printers[n]; ok {
			p := pid
			out[i] = order.ItemRoute{PrinterID: &p, Routing: order.RoutingMatched}
		} else {
			out[i] = order.ItemRoute{Routing: order.RoutingUnmatched}
		}
	}
	return out, nil
}

func TestCast_SizeIsItsOwnField(t *testing.T) {
	o, err := newTestCaster(t).Cast(context.Background(), order.CastInput{Raw: []byte(samplePayload)})
	if err != nil {
		t.Fatal(err)
	}
	it := o.Items()[0]
	if it.Name != "Regular Tomato Cheese Pizza" || it.Size != "12 Medium" {
		t.Fatalf("name/size = %q / %q, want the size split out of the name", it.Name, it.Size)
	}
}

func TestCast_RoutesItemsBeforeTheReceivedEvent(t *testing.T) {
	pizzaPrinter := uuid.New()
	r := &stubRouter{printers: map[string]uuid.UUID{"Regular Tomato Cheese Pizza": pizzaPrinter}}
	c := New(zerolog.Nop(), stubResolver{tenantID: testTenantID, locationID: testLocationID}, r)

	o, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(samplePayload)})
	if err != nil {
		t.Fatal(err)
	}
	if r.gotLoc != testLocationID {
		t.Fatalf("router got location %v, want the resolved store's %v", r.gotLoc, testLocationID)
	}
	if want := []string{"Regular Tomato Cheese Pizza", "Regular Tomato Cheese Pizza", "Brownies"}; len(r.gotNames) != 3 || r.gotNames[2] != want[2] {
		t.Fatalf("router got names %v, want %v (size-free)", r.gotNames, want)
	}

	// The routing must be inside the order.received payload the agent receives.
	evs := o.PullEvents()
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	var snap order.Snapshot
	if err := json.Unmarshal(evs[0].Payload, &snap); err != nil {
		t.Fatal(err)
	}
	pizza, brownies := snap.Items[0], snap.Items[2]
	if pizza.Routing != "matched" || pizza.PrinterID != pizzaPrinter.String() || pizza.Size != "12 Medium" {
		t.Fatalf("pizza routing = %+v", pizza)
	}
	if brownies.Routing != "unmatched" || brownies.PrinterID != "" {
		t.Fatalf("brownies routing = %+v", brownies)
	}
}

func TestCast_RouterErrorFailsTheCast(t *testing.T) {
	r := &stubRouter{err: errors.New("db down")}
	c := New(zerolog.Nop(), stubResolver{tenantID: testTenantID, locationID: testLocationID}, r)
	if _, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(samplePayload)}); err == nil {
		t.Fatal("a routing failure must fail the cast so SQS redelivers")
	}
}
