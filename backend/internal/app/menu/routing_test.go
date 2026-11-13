package menuApp

import (
	"context"
	"errors"
	"testing"
	"time"

	menuDom "quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/order"
	tenancyDom "quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/observability"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type routingRepo struct {
	menuDom.Repository
	entries []menuDom.RouteEntry
}

func (r routingRepo) ListRoutingByLocation(context.Context, uuid.UUID) ([]menuDom.RouteEntry, error) {
	return r.entries, nil
}

func TestRouteItems(t *testing.T) {
	p1, p2 := uuid.New(), uuid.New()
	pizza, pizzaDup, fries, salad := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repo := routingRepo{entries: []menuDom.RouteEntry{ // best candidate first
		{ItemID: pizza, Name: "Pepperoni Pizza", PrinterID: &p1},
		{ItemID: pizzaDup, Name: "pepperoni pizza", PrinterID: &p2}, // loses the collision
		{ItemID: fries, Name: "French  Fries", PrinterID: &p2},
		{ItemID: salad, Name: "Garden Salad"}, // no item/category printer
	}}
	meters, _ := observability.NewMeters()
	svc := NewService(repo, nil, meters, zerolog.Nop())

	got, err := svc.RouteItems(context.Background(), uuid.New(),
		[]string{"PEPPERONI   pizza", " french fries ", "Garden Salad", "Mystery Item"})
	if err != nil {
		t.Fatal(err)
	}
	check := func(i int, routing order.Routing, item, printer *uuid.UUID) {
		t.Helper()
		g := got[i]
		if g.Routing != routing {
			t.Errorf("[%d] routing = %q, want %q", i, g.Routing, routing)
		}
		if (item == nil) != (g.MenuItemID == nil) || (item != nil && *item != *g.MenuItemID) {
			t.Errorf("[%d] menu item = %v, want %v", i, g.MenuItemID, item)
		}
		if (printer == nil) != (g.PrinterID == nil) || (printer != nil && *printer != *g.PrinterID) {
			t.Errorf("[%d] printer = %v, want %v", i, g.PrinterID, printer)
		}
	}
	check(0, order.RoutingMatched, &pizza, &p1) // case/space-insensitive, first candidate wins
	check(1, order.RoutingMatched, &fries, &p2)
	check(2, order.RoutingUnassigned, &salad, nil)
	check(3, order.RoutingUnmatched, nil, nil)
}

type fakeVendor map[string]uuid.UUID

func (f fakeVendor) VendorMaps(context.Context, uuid.UUID) (map[string]uuid.UUID, error) {
	return f, nil
}

// A vendor-view mapping routes a name nothing matches, and wins over a name
// match; a mapping to an item that's gone changes nothing.
func TestRouteItemsVendorMaps(t *testing.T) {
	p1, p2 := uuid.New(), uuid.New()
	wings, pizza := uuid.New(), uuid.New()
	repo := routingRepo{entries: []menuDom.RouteEntry{
		{ItemID: wings, Name: "Wings 8pc", PrinterID: &p1},
		{ItemID: pizza, Name: "Cheese Pizza", PrinterID: &p2},
	}}
	meters, _ := observability.NewMeters()
	svc := NewService(repo, nil, meters, zerolog.Nop())
	svc.SetVendorMapper(fakeVendor{"jumbo party wings": wings, "cheese pizza": wings, "ghost": uuid.New()})
	got, err := svc.RouteItems(context.Background(), uuid.New(), []string{"Jumbo  Party Wings", "Cheese Pizza", "Ghost"})
	if err != nil {
		t.Fatal(err)
	}
	if *got[0].MenuItemID != wings || *got[0].PrinterID != p1 || *got[1].MenuItemID != wings || got[2].Routing != order.RoutingUnmatched {
		t.Errorf("routes: %+v", got)
	}
}

type unmatchedRepo struct {
	routingRepo
	recent []menuDom.ItemNameCount
}

func (r unmatchedRepo) ListRecentItemNames(context.Context, uuid.UUID, uuid.UUID, time.Time) ([]menuDom.ItemNameCount, error) {
	return r.recent, nil
}

func TestUnmatchedNames(t *testing.T) {
	p1 := uuid.New()
	now := time.Now()
	repo := unmatchedRepo{
		routingRepo: routingRepo{entries: []menuDom.RouteEntry{
			{ItemID: uuid.New(), Name: "Pepperoni Pizza", PrinterID: &p1},
			{ItemID: uuid.New(), Name: "Garden Salad"}, // matched, no printer
		}},
		recent: []menuDom.ItemNameCount{
			{Name: "pepperoni  pizza", Orders: 9, LastSeen: now},                // routes fine → not listed
			{Name: "Mystery Special", Orders: 2, LastSeen: now.Add(-time.Hour)}, // unmatched
			{Name: "MYSTERY special", Orders: 5, LastSeen: now},                 // same name, other spelling
			{Name: "Garden Salad", Orders: 1, LastSeen: now},                    // unassigned
		},
	}
	meters, _ := observability.NewMeters()
	got, err := NewService(repo, nil, meters, zerolog.Nop()).UnmatchedNames(context.Background(), uuid.New(), uuid.New(), now.Add(-time.Hour*24))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Name != "MYSTERY special" || got[0].Orders != 7 || got[0].Routing != "unmatched" || !got[0].LastSeen.Equal(now) {
		t.Fatalf("merged unmatched: %+v", got[0])
	}
	if got[1].Name != "Garden Salad" || got[1].Routing != "unassigned" {
		t.Fatalf("unassigned: %+v", got[1])
	}
}

type brokenVendor struct{}

func (brokenVendor) VendorMaps(context.Context, uuid.UUID) (map[string]uuid.UUID, error) {
	return nil, errors.New("db down")
}

// A failing mapping lookup never blocks ingest: routing falls back to names.
func TestRouteItemsVendorMapsDown(t *testing.T) {
	p1, wings := uuid.New(), uuid.New()
	meters, _ := observability.NewMeters()
	svc := NewService(routingRepo{entries: []menuDom.RouteEntry{{ItemID: wings, Name: "Wings 8pc", PrinterID: &p1}}}, nil, meters, zerolog.Nop())
	svc.SetVendorMapper(brokenVendor{})
	got, err := svc.RouteItems(context.Background(), uuid.New(), []string{"wings 8pc"})
	if err != nil || *got[0].MenuItemID != wings {
		t.Errorf("routes %+v err %v", got, err)
	}
}

// "No kitchen ticket" (00041): a name / bank item that matches such an item
// is routed "none" with no printer; the counter menu's routes say
// {printer_id: "", no_kitchen: true}; it's never in the unmatched report.
func TestRoutingNoKitchen(t *testing.T) {
	p1 := uuid.New()
	ranch, coke, salad := uuid.New(), uuid.New(), uuid.New()
	repo := routingRepo{entries: []menuDom.RouteEntry{
		{ItemID: ranch, Name: "Ranch", PrinterID: &p1},
		{ItemID: coke, Name: "Coke 2 Liter", NoKitchen: true},
		{ItemID: salad, Name: "Garden Salad"},
	}}
	meters, _ := observability.NewMeters()
	svc := NewService(repo, nil, meters, zerolog.Nop())
	ctx := context.Background()

	got, err := svc.RouteItems(ctx, uuid.New(), []string{"coke 2 liter", "Ranch", "Garden Salad"})
	if err != nil {
		t.Fatal(err)
	}
	if g := got[0]; g.Routing != order.RoutingNoKitchen || g.PrinterID != nil || g.MenuItemID == nil || *g.MenuItemID != coke {
		t.Errorf("coke = %+v, want routing none, no printer", g)
	}
	if got[1].Routing != order.RoutingMatched || got[2].Routing != order.RoutingUnassigned {
		t.Errorf("others = %+v", got)
	}
	byID, err := svc.RouteBankItems(ctx, uuid.New(), []uuid.UUID{coke, ranch})
	if err != nil {
		t.Fatal(err)
	}
	if byID[0].Routing != order.RoutingNoKitchen || byID[0].PrinterID != nil || byID[1].Routing != order.RoutingMatched {
		t.Errorf("bank items = %+v", byID)
	}
	if string(order.RoutingNoKitchen) != "none" {
		t.Errorf("the wire value is %q, want none", order.RoutingNoKitchen)
	}

	svc.listPrinters = func(context.Context, uuid.UUID) ([]tenancyDom.Printer, error) {
		return []tenancyDom.Printer{{ID: p1, Name: "Wings"}}, nil
	}
	routes, err := svc.ItemPrinters(ctx, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if r := routes[coke]; !r.NoKitchen || r.PrinterID != "" {
		t.Errorf("coke route = %+v", r)
	}
	if r := routes[ranch]; r.NoKitchen || r.PrinterID != p1.String() || r.Name != "Wings" {
		t.Errorf("ranch route = %+v", r)
	}
	if _, ok := routes[salad]; ok {
		t.Errorf("salad has a route, want none (prints on every station)")
	}
}

// The Quick add item (00008) is the terminal's route only: an online line
// named like it is not matched to it, and a real item of the same name still is.
func TestRouteItemsSkipsQuickAdd(t *testing.T) {
	p1 := uuid.New()
	qa, real := uuid.New(), uuid.New()
	repo := routingRepo{entries: []menuDom.RouteEntry{
		{ItemID: qa, Name: "Quick add", PrinterID: &p1, QuickAdd: true},
		{ItemID: uuid.New(), Name: "Open item", PrinterID: &p1, QuickAdd: false},
		{ItemID: real, Name: "Quick Add", PrinterID: &p1}, // a later twin that is a real item
	}}
	meters, _ := observability.NewMeters()
	svc := NewService(repo, nil, meters, zerolog.Nop())
	got, err := svc.RouteItems(context.Background(), uuid.New(), []string{"quick add"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].MenuItemID == nil || *got[0].MenuItemID != real {
		t.Errorf("quick add matched %v, want the real item %v", got[0].MenuItemID, real)
	}
	repo.entries = repo.entries[:1]
	svc = NewService(repo, nil, meters, zerolog.Nop())
	if got, _ = svc.RouteItems(context.Background(), uuid.New(), []string{"Quick add"}); got[0].Routing != order.RoutingUnmatched {
		t.Errorf("only the Quick add item: %+v, want unmatched", got[0])
	}
}
