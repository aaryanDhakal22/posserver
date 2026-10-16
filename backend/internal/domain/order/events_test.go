package order

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

// stubInput returns a NewOrderInput valid enough for NewOrder to succeed.
func stubInput() NewOrderInput {
	return NewOrderInput{
		TenantID:      TenantID(uuid.MustParse("11111111-1111-1111-1111-111111111111")),
		LocationID:    LocationID(uuid.MustParse("22222222-2222-2222-2222-222222222222")),
		Source:        SourceOnline,
		SourceEventID: "test-1",
		Currency:      "USD",
		PlacedAt:      time.Now().UTC(),
		Items: []LineItem{{
			ID:        uuid.Must(uuid.NewV7()),
			Position:  0,
			Name:      "Test Item",
			Quantity:  1,
			UnitPrice: 1000,
			LineTotal: 1000,
		}},
		Money: MoneyBreakdown{
			Subtotal: 1000,
			Tax:      0,
			Tip:      0,
			Total:    1000,
		},
		RawPayload: []byte("{}"),
	}
}

func TestNewOrder_EmitsOrderReceived(t *testing.T) {
	o, err := NewOrder(stubInput())
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	if got := o.PendingEventCount(); got != 1 {
		t.Fatalf("PendingEventCount = %d, want 1", got)
	}
	events := o.PullEvents()
	if got := events[0].Type; got != EventOrderReceived {
		t.Errorf("first event Type = %q, want %q", got, EventOrderReceived)
	}
	if got := events[0].AggregateID; got != o.ID() {
		t.Errorf("event AggregateID = %v, want %v", got, o.ID())
	}
	if got := o.PendingEventCount(); got != 0 {
		t.Errorf("PendingEventCount after PullEvents = %d, want 0", got)
	}

	// Fat payload contract: OrderReceived carries the full Snapshot. Decode
	// + spot-check a couple of fields rather than the whole struct.
	var snap Snapshot
	if err := json.Unmarshal(events[0].Payload, &snap); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if snap.Source != string(SourceOnline) {
		t.Errorf("payload Source = %q, want %q", snap.Source, SourceOnline)
	}
	if snap.Status != string(StatusReceived) {
		t.Errorf("payload Status = %q, want %q", snap.Status, StatusReceived)
	}
	if snap.ID != uuid.UUID(o.ID()).String() {
		t.Errorf("payload ID = %q, want %q", snap.ID, uuid.UUID(o.ID()).String())
	}
	if len(snap.Items) != 1 {
		t.Errorf("payload Items count = %d, want 1", len(snap.Items))
	}
}

func TestPullEvents_IsIdempotent(t *testing.T) {
	o, _ := NewOrder(stubInput())
	first := o.PullEvents()
	second := o.PullEvents()
	if len(first) != 1 {
		t.Errorf("first PullEvents = %d, want 1", len(first))
	}
	if second != nil {
		t.Errorf("second PullEvents = %v, want nil", second)
	}
}

func TestTransition_EmitsStatusChanged(t *testing.T) {
	o, _ := NewOrder(stubInput())
	_ = o.PullEvents() // drain the OrderReceived

	if err := o.Transition(StatusValidated, "system", uuid.UUID{}, ""); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	events := o.PullEvents()
	if len(events) != 1 {
		t.Fatalf("expected 1 event after Transition, got %d", len(events))
	}
	if events[0].Type != EventOrderStatusChanged {
		t.Errorf("Type = %q, want %q", events[0].Type, EventOrderStatusChanged)
	}
	var p StatusChangedPayload
	if err := json.Unmarshal(events[0].Payload, &p); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if p.From != "received" {
		t.Errorf("Payload.from = %q, want received", p.From)
	}
	if p.To != "validated" {
		t.Errorf("Payload.to = %q, want validated", p.To)
	}
	// Post-transition snapshot reflects the new status.
	if p.Snapshot.Status != "validated" {
		t.Errorf("Payload.snapshot.Status = %q, want validated", p.Snapshot.Status)
	}
}

func TestTransition_InvalidDoesNotEmitEvent(t *testing.T) {
	o, _ := NewOrder(stubInput())
	_ = o.PullEvents()

	// Received → Completed is not a valid transition.
	if err := o.Transition(StatusCompleted, "system", uuid.UUID{}, ""); err == nil {
		t.Fatal("expected ErrInvalidTransition")
	}
	if got := o.PendingEventCount(); got != 0 {
		t.Errorf("invalid transition emitted %d events; want 0", got)
	}
}

func TestRehydrate_HasNoPendingEvents(t *testing.T) {
	o := Rehydrate(RehydrateInput{
		ID:         ID(uuid.Must(uuid.NewV7())),
		Source:     SourceOnline,
		Status:     StatusReceived,
		Currency:   "USD",
		PlacedAt:   time.Now(),
		ReceivedAt: time.Now(),
		UpdatedAt:  time.Now(),
	})
	if got := o.PendingEventCount(); got != 0 {
		t.Errorf("Rehydrated aggregate has %d pending events; want 0", got)
	}
}
