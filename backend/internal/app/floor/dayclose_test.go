package floor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	daycloseApp "quiccpos/main/internal/app/dayclose"
	"quiccpos/main/internal/domain/dayclose"
	"quiccpos/main/internal/domain/order"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/observability"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// fakeDayClose keeps what the day-close service stores (in memory).
type fakeDayClose struct {
	daycloseApp.Repo
	drawers map[uuid.UUID]dayclose.Drawer
	moves   map[uuid.UUID]dayclose.Movement
	zs      map[int64]dayclose.Z
}

func (f *fakeDayClose) OpenDrawer(_ context.Context, _ order.Scope, _ uuid.UUID, _ string, d dayclose.Drawer) (string, error) {
	if cur, ok := f.drawers[d.ID]; ok && cur.ClosedAt != nil {
		return order.FloorStale, nil
	}
	f.drawers[d.ID] = d
	return order.FloorApplied, nil
}

func (f *fakeDayClose) CloseDrawer(_ context.Context, _ order.Scope, _ uuid.UUID, _ string, d dayclose.Drawer) (string, error) {
	f.drawers[d.ID] = d
	return order.FloorApplied, nil
}

func (f *fakeDayClose) AddMovement(_ context.Context, _ order.Scope, _ uuid.UUID, _ string, m dayclose.Movement) (string, error) {
	if _, ok := f.moves[m.ID]; ok {
		return order.FloorDuplicate, nil
	}
	f.moves[m.ID] = m
	return order.FloorApplied, nil
}

func (f *fakeDayClose) SaveZ(_ context.Context, _ order.Scope, _ uuid.UUID, _ string, z dayclose.Z) (string, error) {
	f.zs[z.BatchID] = z
	return order.FloorApplied, nil
}

func TestDayCloseEvents(t *testing.T) {
	meters, err := observability.NewMeters()
	if err != nil {
		t.Fatal(err)
	}
	repo := &fakeFloor{seen: map[uuid.UUID]string{}}
	dc := &fakeDayClose{drawers: map[uuid.UUID]dayclose.Drawer{}, moves: map[uuid.UUID]dayclose.Movement{}, zs: map[int64]dayclose.Z{}}
	svc := NewService(noOrders{}, repo, zerolog.Nop(), meters)
	scope := sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}
	at := time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC)
	drawerID := uuid.New()
	drawer := map[string]any{"id": drawerID, "device_id": "dev-1", "device_name": "Till 1", "staff_id": uuid.NewString(),
		"staff_name": "Cara S.", "batch_id": 4, "opened_at": at, "start_cents": 15000}
	closedDrawer := map[string]any{}
	for k, v := range drawer {
		closedDrawer[k] = v
	}
	closedDrawer["closed_at"], closedDrawer["close_reason"], closedDrawer["counted_cents"] = at.Add(5*time.Hour), "counted", 20000
	closedDrawer["expected_cents"], closedDrawer["over_short_cents"] = 20100, -100
	ev := func(typ string, kv ...any) map[string]any {
		m := map[string]any{"event_id": uuid.NewString(), "type": typ, "at": at}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	opened := ev("drawer.opened", "drawer", drawer)
	move := ev("drawer.movement", "movement", map[string]any{"id": uuid.New(), "drawer_id": drawerID, "kind": "pay_out",
		"amount_cents": 1500, "reason": "ice", "staff_id": "s", "at": at.Add(time.Hour)})
	badMove := ev("drawer.movement", "movement", map[string]any{"id": uuid.New(), "drawer_id": drawerID, "kind": "pay_out",
		"amount_cents": 0, "at": at})
	closed := ev("drawer.closed", "drawer", closedDrawer)
	lateOpened := ev("drawer.opened", "drawer", drawer)
	z := ev("z.report", "report", map[string]any{"id": uuid.New(), "batch_id": 4, "business_date": "2026-11-11", "version": 1})
	badZ := ev("z.report", "report", map[string]any{"id": uuid.New(), "business_date": "2026-11-11"})
	noBody := ev("drawer.closed")

	handle := func(events ...any) []Result {
		raw, _ := json.Marshal(map[string]any{"events": events})
		out, err := svc.Handle(context.Background(), scope, raw)
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)["results"].([]Result)
	}
	// No day-close store plugged in (an older main): invalid.
	if got := handle(ev("drawer.opened", "drawer", drawer)); got[0].Result != order.FloorInvalid {
		t.Fatalf("without a store: %v", got)
	}
	svc.OnDayClose(daycloseApp.NewService(dc, func(error) bool { return false }, zerolog.Nop()))
	got := handle(opened, move, badMove, closed, lateOpened, z, badZ, noBody)
	want := []string{order.FloorApplied, order.FloorApplied, order.FloorInvalid, order.FloorApplied, order.FloorStale,
		order.FloorApplied, order.FloorInvalid, order.FloorInvalid}
	for i, w := range want {
		if got[i].Result != w {
			t.Errorf("event %d: %s, want %s", i, got[i].Result, w)
		}
	}
	if d := dc.drawers[drawerID]; d.ClosedAt == nil || *d.OverShortCents != -100 {
		t.Fatalf("the closed drawer was overwritten: %+v", d)
	}
	if len(dc.moves) != 1 || dc.zs[4].BusinessDate.Format(time.DateOnly) != "2026-11-11" {
		t.Fatalf("moves %d, z %+v", len(dc.moves), dc.zs[4])
	}
	// Refused events are marked seen as invalid (the Pi keeps / parks them).
	for _, e := range []map[string]any{badMove, badZ, noBody} {
		if repo.seen[uuid.MustParse(e["event_id"].(string))] != order.FloorInvalid {
			t.Errorf("%s not marked invalid", e["type"])
		}
	}
}
