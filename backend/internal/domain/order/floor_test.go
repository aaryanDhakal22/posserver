package order

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFloorStatusMapping(t *testing.T) {
	for in, want := range map[string]Status{"accepted": StatusAccepted, "ready": StatusReady,
		"on_delivery": StatusOnDelivery, "delivered": StatusCompleted, "cancelled": StatusCancelled} {
		if got, ok := FloorStatus(in); !ok || got != want {
			t.Errorf("%s → %s %v", in, got, ok)
		}
	}
	if _, ok := FloorStatus("arrived"); ok {
		t.Error("arrived is not a floor status main applies")
	}
}

func TestApplyFloorUpdateForwardOnly(t *testing.T) {
	o, _ := NewOrder(stubInput())
	o.PullEvents()
	k := 1104
	eta := time.Now().Add(20 * time.Minute).UTC().Truncate(time.Second)

	if r := o.ApplyFloorUpdate(FloorUpdate{Status: StatusAccepted, KitchenNumber: &k, EstimatedReadyAt: &eta, At: time.Now()}); r != FloorApplied {
		t.Fatalf("accept: %s", r)
	}
	if o.Status() != StatusAccepted || *o.KitchenNumber() != 1104 || !o.EstimatedReadyAt().Equal(eta) {
		t.Fatalf("after accept: %s %v %v", o.Status(), o.KitchenNumber(), o.EstimatedReadyAt())
	}
	evs := o.PullEvents()
	if len(evs) != 1 || evs[0].Type != EventOrderStatusChanged {
		t.Fatalf("events: %+v", evs)
	}
	var p StatusChangedPayload
	json.Unmarshal(evs[0].Payload, &p)
	if p.From != "received" || p.To != "accepted" || p.Actor != "agent" || p.Snapshot.KitchenNumber == nil {
		t.Fatalf("payload: %+v", p)
	}

	// Skipping steps is fine; going backwards is stale.
	if r := o.ApplyFloorUpdate(FloorUpdate{Status: StatusOnDelivery, DriverName: "Sam"}); r != FloorApplied {
		t.Fatalf("on_delivery: %s", r)
	}
	if r := o.ApplyFloorUpdate(FloorUpdate{Status: StatusReady}); r != FloorStale {
		t.Fatalf("late ready must be stale: %s", r)
	}
	if o.Status() != StatusOnDelivery || o.DriverName() != "Sam" {
		t.Fatalf("state: %s %s", o.Status(), o.DriverName())
	}
	if r := o.ApplyFloorUpdate(FloorUpdate{Status: StatusCompleted, Auto: true}); r != FloorApplied {
		t.Fatalf("delivered: %s", r)
	}
	h := o.StatusHistory()
	last := h[len(h)-1]
	if last.To != StatusCompleted || last.Actor != "agent" || last.Reason != "auto-finished at batch close" {
		t.Fatalf("history: %+v", last)
	}
	// Same update again: nothing changes.
	if r := o.ApplyFloorUpdate(FloorUpdate{Status: StatusCompleted}); r != FloorStale {
		t.Fatalf("replay: %s", r)
	}
}

func TestApplyFloorUpdateNeverRevivesCancelled(t *testing.T) {
	o, _ := NewOrder(stubInput())
	o.Transition(StatusRejected, "system", [16]byte{}, "bad")
	if r := o.ApplyFloorUpdate(FloorUpdate{Status: StatusAccepted}); r != FloorStale || o.Status() != StatusRejected {
		t.Fatalf("rejected order changed: %s %s", r, o.Status())
	}
}

func TestApplyFloorCancelOnlyForUnacceptedOrders(t *testing.T) {
	o, _ := NewOrder(stubInput())
	o.PullEvents()
	if r := o.ApplyFloorUpdate(FloorUpdate{Status: StatusCancelled, Auto: true}); r != FloorApplied || o.Status() != StatusCancelled {
		t.Fatalf("cancel a received order: %s %s", r, o.Status())
	}
	h := o.StatusHistory()
	if last := h[len(h)-1]; last.Actor != "agent" || last.Reason == "" {
		t.Fatalf("history: %+v", last)
	}
	if evs := o.PullEvents(); len(evs) != 1 {
		t.Fatalf("events: %d", len(evs))
	}
	if r := o.ApplyFloorUpdate(FloorUpdate{Status: StatusAccepted}); r != FloorStale {
		t.Fatalf("a cancelled order stays cancelled: %s", r)
	}

	accepted, _ := NewOrder(stubInput())
	accepted.ApplyFloorUpdate(FloorUpdate{Status: StatusAccepted})
	if r := accepted.ApplyFloorUpdate(FloorUpdate{Status: StatusCancelled}); r != FloorStale || accepted.Status() != StatusAccepted {
		t.Fatalf("an accepted order can't be floor-cancelled: %s %s", r, accepted.Status())
	}
}

// An older event (late / overlapping delivery) can't bring back stale field
// values once a newer one was applied.
func TestApplyFloorUpdateIgnoresStaleFields(t *testing.T) {
	o, _ := NewOrder(stubInput())
	t0 := time.Now().UTC()
	newEta, oldEta := t0.Add(30*time.Minute), t0.Add(15*time.Minute)
	if r := o.ApplyFloorUpdate(FloorUpdate{Status: StatusAccepted, EstimatedReadyAt: &newEta, At: t0.Add(time.Minute)}); r != FloorApplied {
		t.Fatal(r)
	}
	if r := o.ApplyFloorUpdate(FloorUpdate{Status: StatusAccepted, EstimatedReadyAt: &oldEta, At: t0}); r != FloorStale {
		t.Fatalf("older event: %s", r)
	}
	if !o.EstimatedReadyAt().Equal(newEta) || o.FloorAt() == nil {
		t.Fatalf("eta = %v", o.EstimatedReadyAt())
	}
	// A newer status still moves forward even if its timestamp is older.
	if r := o.ApplyFloorUpdate(FloorUpdate{Status: StatusReady, At: t0}); r != FloorApplied || o.Status() != StatusReady {
		t.Fatalf("status: %s %s", r, o.Status())
	}
}

func TestFloorScheduledRanksBetweenValidatedAndAccepted(t *testing.T) {
	if got, ok := FloorStatus("scheduled"); !ok || got != StatusScheduled {
		t.Fatalf("FloorStatus(scheduled) = %s %v", got, ok)
	}
	if !(floorRank[StatusValidated] < floorRank[StatusScheduled] && floorRank[StatusScheduled] < floorRank[StatusAccepted]) {
		t.Fatalf("ranks: validated %d, scheduled %d, accepted %d", floorRank[StatusValidated], floorRank[StatusScheduled], floorRank[StatusAccepted])
	}

	o, _ := NewOrder(stubInput())
	o.PullEvents()
	t0 := time.Now().UTC()
	if r := o.ApplyFloorUpdate(FloorUpdate{Status: StatusScheduled, At: t0}); r != FloorApplied || o.Status() != StatusScheduled {
		t.Fatalf("schedule: %s %s", r, o.Status())
	}
	evs := o.PullEvents()
	var p StatusChangedPayload
	if len(evs) != 1 || json.Unmarshal(evs[0].Payload, &p) != nil || p.From != "received" || p.To != "scheduled" {
		t.Fatalf("events: %+v", evs)
	}
	// A late "scheduled" after accept never moves the order back.
	k := 1101
	if r := o.ApplyFloorUpdate(FloorUpdate{Status: StatusAccepted, KitchenNumber: &k, At: t0.Add(time.Minute)}); r != FloorApplied || o.Status() != StatusAccepted {
		t.Fatalf("accept after schedule: %s %s", r, o.Status())
	}
	if r := o.ApplyFloorUpdate(FloorUpdate{Status: StatusScheduled, At: t0.Add(2 * time.Minute)}); r != FloorStale || o.Status() != StatusAccepted {
		t.Fatalf("late scheduled: %s %s", r, o.Status())
	}

	// A scheduled order nobody accepted can still be cancelled.
	o2, _ := NewOrder(stubInput())
	o2.ApplyFloorUpdate(FloorUpdate{Status: StatusScheduled, At: t0})
	if r := o2.ApplyFloorUpdate(FloorUpdate{Status: StatusCancelled, At: t0.Add(time.Hour)}); r != FloorApplied || o2.Status() != StatusCancelled {
		t.Fatalf("cancel scheduled: %s %s", r, o2.Status())
	}
}

func TestValidRunStops(t *testing.T) {
	cases := []struct {
		raw string
		ok  bool
		n   int
	}{
		{``, true, 0},
		{`null`, true, 0},
		{`[]`, true, 0},
		{`[{"order_id":"o1","eta":"2026-10-25T18:00:00Z"}]`, true, 1},
		{`[{"opaque_id":"pos:0123456789ab","source":"pos","placed_at":"2026-10-25T17:40:00Z","sent_at":"2026-10-25T17:41:00Z","out_at":"2026-10-25T18:00:00Z","delivered_at":null,"distance_m":2400}]`, true, 1},
		{`[{"order_id":"o1"},{"opaque_id":"pos:ab","source":"pos"}]`, true, 2},
		{`[{"eta":"x"}]`, false, 0},
		{`[null]`, false, 0},
		{`{"order_id":"o1"}`, false, 0},
		{`["o1"]`, false, 0},
	}
	for _, c := range cases {
		ok, n := ValidRunStops(json.RawMessage(c.raw))
		if ok != c.ok || n != c.n {
			t.Errorf("%s: ok=%v n=%d, want %v %d", c.raw, ok, n, c.ok, c.n)
		}
	}
}
