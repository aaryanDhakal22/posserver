package menuApp

import (
	"context"
	"errors"
	"testing"

	menuDom "quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/observability"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type counterVendor struct {
	maps map[string]uuid.UUID
	only map[uuid.UUID]bool
	err  error
}

func (f counterVendor) VendorMaps(context.Context, uuid.UUID) (map[string]uuid.UUID, error) {
	return f.maps, nil
}

func (f counterVendor) CounterOnlyItems(context.Context, uuid.UUID) (map[uuid.UUID]bool, error) {
	return f.only, f.err
}

// An online order never routes to an item only the counter sells: not by
// name, not by a vendor map; the website's item of that name still matches.
func TestRouteItemsSkipsCounterOnly(t *testing.T) {
	p1 := uuid.New()
	counterPizza, counterKnots, webWings := uuid.New(), uuid.New(), uuid.New()
	repo := routingRepo{entries: []menuDom.RouteEntry{
		{ItemID: counterPizza, Name: "Cheese Pizza", PrinterID: &p1},
		{ItemID: counterKnots, Name: "Garlic Knots", PrinterID: &p1},
		{ItemID: webWings, Name: "Wings", PrinterID: &p1},
	}}
	meters, _ := observability.NewMeters()
	svc := NewService(repo, nil, meters, zerolog.Nop())
	svc.SetVendorMapper(counterVendor{maps: map[string]uuid.UUID{"knots": counterKnots},
		only: map[uuid.UUID]bool{counterPizza: true, counterKnots: true}})
	got, err := svc.RouteItems(context.Background(), uuid.New(), []string{"Cheese Pizza", "Knots", "Wings"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Routing != order.RoutingUnmatched || got[1].Routing != order.RoutingUnmatched || got[2].Routing != order.RoutingMatched {
		t.Errorf("routes: %+v", got)
	}

	svc.SetVendorMapper(counterVendor{err: errors.New("db down")})
	if _, err := svc.RouteItems(context.Background(), uuid.New(), []string{"Wings"}); err == nil {
		t.Error("a counter-only lookup failure must fail the routing, not route by name")
	}
}
