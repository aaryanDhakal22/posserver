package menuApp

import (
	"context"
	"testing"
	"time"

	menuDom "quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/order"
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
			{Name: "pepperoni  pizza", Orders: 9, LastSeen: now},               // routes fine → not listed
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
