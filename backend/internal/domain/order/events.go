package order

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// EventType is a stable string identifier for a domain event. Stored in the
// outbox `type` column. Adding a new type is additive — handlers that don't
// recognize it should skip rather than error.
type EventType string

const (
	// EventOrderReceived fires when an aggregate is constructed via NewOrder.
	// Payload: order.Snapshot (the whole order at receive time). Subscribers
	// (SSE, push notifications, audit) drive their first action off this.
	EventOrderReceived EventType = "order.received"

	// EventOrderStatusChanged fires on every successful Transition.
	// Payload: order.StatusChangedPayload (from/to/actor/reason + the
	// post-transition Snapshot). Subscribers can observe lifecycle progress
	// and have the full state to act on without a DB read.
	EventOrderStatusChanged EventType = "order.status_changed"
)

// DomainEvent is the in-memory representation of an event the aggregate has
// produced but not yet persisted. The repo writes these into the outbox in
// the same transaction as the aggregate state, satisfying the
// transactional-outbox guarantee: the event exists iff the state change is
// committed.
//
// Payload is the marshaled JSON for the event's typed payload. Each
// EventType has a corresponding Go type whose marshaled bytes go here:
//
//	EventOrderReceived       → Snapshot
//	EventOrderStatusChanged  → StatusChangedPayload
//
// We carry the bytes (not a typed value) because the outbox stores it as
// jsonb and we want to avoid an extra marshal/unmarshal round trip on the
// repo path. Handlers Unmarshal into the matching type at handle time.
type DomainEvent struct {
	ID            uuid.UUID
	AggregateID   ID
	Type          EventType
	Payload       json.RawMessage
	OccurredAt    time.Time
	SchemaVersion int
}

// recordEvent serializes a typed payload and appends a fresh DomainEvent to
// the aggregate's pending list. Always called from inside aggregate methods
// (NewOrder, Transition) — never from the outside.
//
// Marshal failures here would mean a programmer wrote a payload struct
// with a non-marshalable field; that's a build-time bug, not a runtime
// condition the system needs to handle. We surface as an empty payload
// (the aggregate is still valid) but the upstream tests will catch it.
func (o *Order) recordEvent(t EventType, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte("null")
	}
	o.pendingEvents = append(o.pendingEvents, DomainEvent{
		ID:            uuid.Must(uuid.NewV7()),
		AggregateID:   o.id,
		Type:          t,
		Payload:       raw,
		OccurredAt:    time.Now().UTC(),
		SchemaVersion: o.schemaVersion,
	})
}

// PullEvents returns the aggregate's accumulated DomainEvents and clears
// the internal buffer. Called by the repository inside the persist
// transaction; the events are written to the outbox alongside the state
// change. After PullEvents the aggregate has no pending events.
//
// This MUST be called by exactly one caller (the repo). Calling it from
// anywhere else loses events.
func (o *Order) PullEvents() []DomainEvent {
	if len(o.pendingEvents) == 0 {
		return nil
	}
	out := o.pendingEvents
	o.pendingEvents = nil
	return out
}

// PendingEventCount is for tests and diagnostics. The repo should always
// drain to zero; a non-zero count after Create is a bug.
func (o *Order) PendingEventCount() int { return len(o.pendingEvents) }
