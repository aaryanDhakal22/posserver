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
