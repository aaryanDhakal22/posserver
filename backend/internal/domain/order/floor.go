package order

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Floor flow: the restaurant's agent owns an order's life on the floor and
// pushes every change here as a floor event. main is the read model — it
// applies updates forward-only (a late or replayed event can't move an order
// backwards) and never invents floor state itself.

// Floor results reported back to the agent per event.
const (
	FloorApplied   = "applied"   // state or fields changed
	FloorDuplicate = "duplicate" // this event_id was already applied
	FloorStale     = "stale"     // nothing newer than what main has
	FloorNotFound  = "not_found" // no such order in the agent's scope
	FloorInvalid   = "invalid"   // malformed / unknown status
)

// floorRank orders the lifecycle for forward-only application. Rejected /
// cancelled are terminal and never overridden by the floor.
var floorRank = map[Status]int{
	StatusReceived:   0,
	StatusValidated:  1,
	StatusScheduled:  2, // 2.1.0: a future order, confirmed but not yet accepted
	StatusAccepted:   3,
	StatusPreparing:  4,
	StatusReady:      5,
	StatusOnDelivery: 6,
	StatusCompleted:  7,
}

// FloorStatus maps an agent floor state to main's status. delivered →
// completed; cancelled (never accepted, closed out with the batch) →
// cancelled; scheduled (a future order, 2.1.0) → scheduled; unknown → false.
func FloorStatus(agentState string) (Status, bool) {
	switch agentState {
	case "scheduled":
		return StatusScheduled, true
	case "accepted":
		return StatusAccepted, true
	case "ready":
		return StatusReady, true
	case "on_delivery":
		return StatusOnDelivery, true
	case "delivered":
		return StatusCompleted, true
	case "cancelled":
		return StatusCancelled, true
	}
	return "", false
}

// FloorUpdate is one agent-reported change.
type FloorUpdate struct {
	Status           Status
	KitchenNumber    *int
	EstimatedReadyAt *time.Time
	DriverName       string
	At               time.Time
	Auto             bool // close-batch auto-finish
}

// ApplyFloorUpdate moves the order forward to u.Status (skipping steps is
// fine — the floor has no "validated"/"preparing") and mirrors the floor
// fields. Returns FloorApplied when anything changed (an order.status_changed
// event is recorded) or FloorStale when nothing did.
func (o *Order) ApplyFloorUpdate(u FloorUpdate) string {
	if u.Status == StatusCancelled {
		return o.applyFloorCancel(u)
	}
	changed := false
	from := o.status
	cur, curOK := floorRank[o.status]
	next, nextOK := floorRank[u.Status]
	at := u.At.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if curOK && nextOK && next > cur {
		reason := "floor"
		if u.Auto {
			reason = "auto-finished at batch close"
		}
		o.statusHistory = append(o.statusHistory, StatusEvent{From: from, To: u.Status, At: at, Actor: "agent", Reason: reason})
		o.status = u.Status
		changed = true
	}
	if !curOK { // rejected / cancelled: terminal
		return FloorStale
	}
	// Fields only move forward in time: an event older than the newest one
	// applied (a late / overlapping delivery) can't bring back old values.
	if o.floorAt != nil && at.Before(*o.floorAt) {
		if !changed {
			return FloorStale
		}
		o.updatedAt = time.Now().UTC()
		o.recordEvent(EventOrderStatusChanged, StatusChangedPayload{
			From: string(from), To: string(o.status), Actor: "agent", Snapshot: o.Snapshot(),
		})
		return FloorApplied
	}
	if o.floorAt == nil || at.After(*o.floorAt) {
		t := at
		o.floorAt = &t
	}
	if u.KitchenNumber != nil && (o.kitchenNumber == nil || *o.kitchenNumber != *u.KitchenNumber) {
		k := *u.KitchenNumber
		o.kitchenNumber = &k
		changed = true
	}
	if u.EstimatedReadyAt != nil && (o.estimatedReadyAt == nil || !o.estimatedReadyAt.Equal(*u.EstimatedReadyAt)) {
		t := u.EstimatedReadyAt.UTC()
		o.estimatedReadyAt = &t
		changed = true
	}
	if u.DriverName != "" && u.DriverName != o.driverName {
		o.driverName = u.DriverName
		changed = true
	}
	if !changed {
		return FloorStale
	}
	o.updatedAt = time.Now().UTC()
	o.recordEvent(EventOrderStatusChanged, StatusChangedPayload{
		From:     string(from),
		To:       string(o.status),
		Actor:    "agent",
		Snapshot: o.Snapshot(),
	})
	return FloorApplied
}

// applyFloorCancel: the agent cancels an order nobody accepted when it closes
// the batch. Only an order main still has as not-yet-accepted (incl. a
// scheduled future order) can be cancelled this way; anything further along
// (or already terminal) is stale.
func (o *Order) applyFloorCancel(u FloorUpdate) string {
	if o.status != StatusReceived && o.status != StatusValidated && o.status != StatusScheduled {
		return FloorStale
	}
	at := u.At.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	from := o.status
	reason := "not accepted before the batch was closed"
	o.statusHistory = append(o.statusHistory, StatusEvent{From: from, To: StatusCancelled, At: at, Actor: "agent", Reason: reason})
	o.status = StatusCancelled
	o.updatedAt = time.Now().UTC()
	o.recordEvent(EventOrderStatusChanged, StatusChangedPayload{
		From: string(from), To: string(StatusCancelled), Actor: "agent", Reason: reason, Snapshot: o.Snapshot(),
	})
	return FloorApplied
}

// DeliveryRun is one run a driver was sent out on (agent floor event
// delivery.run, 2.0.9): AUTO's suggestion vs what was actually sent, kept for
// tuning past the Pi's batch retention.
type DeliveryRun struct {
	ID           uuid.UUID // the floor event id
	BatchID      *int64
	DriverID     string
	DriverName   string
	PlanID       string // "" when sent without an AUTO plan
	SuggestedIDs []string
	SentIDs      []string
	// Stops: [{order_id, eta}] for online orders; a POS stop (2.1.0) is
	// anonymous — {opaque_id, source:"pos", placed_at, sent_at, out_at,
	// delivered_at, distance_m} — and stored as sent (no order lookup).
	Stops json.RawMessage
	At    time.Time
}

// ValidRunStops reports whether a delivery.run stops value is a JSON array of
// objects, and whether it holds at least one stop. A POS-only run (2.1.0) may
// have no sent_ids — its stops carry opaque ids instead.
func ValidRunStops(raw json.RawMessage) (ok bool, n int) {
	if len(raw) == 0 || string(raw) == "null" {
		return true, 0
	}
	var stops []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &stops); err != nil {
		return false, 0
	}
	for _, s := range stops {
		if s == nil {
			return false, 0
		}
		_, hasOrder := s["order_id"]
		_, hasOpaque := s["opaque_id"]
		if !hasOrder && !hasOpaque {
			return false, 0
		}
	}
	return true, len(stops)
}
