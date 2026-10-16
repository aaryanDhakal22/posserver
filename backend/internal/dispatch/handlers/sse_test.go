package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"quiccpos/main/internal/dispatch"
	"quiccpos/main/internal/domain/order"
	sseBroker "quiccpos/main/internal/infra/sse"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// fakePublisher implements SSEPublisher.
type fakePublisher struct {
	subscribers   int
	delivered     int
	publishCalled bool
	lastBytes     []byte
}

func (f *fakePublisher) SubscribersFor(_ sseBroker.Scope) int { return f.subscribers }
func (f *fakePublisher) Publish(_ context.Context, _ sseBroker.Scope, frame sseBroker.Frame) int {
	f.publishCalled = true
	f.lastBytes = frame.Data
	return f.delivered
}

// fixedScope is reused so the marshaled Snapshot has a parseable tenant_id /
// location_id pair — the dispatch handler now parses these to route via the
// broker.
var (
	fixedTenantID   = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	fixedLocationID = uuid.MustParse("22222222-2222-2222-2222-222222222222")
)

// snapshotPayload returns a marshaled order.Snapshot suitable for stuffing
// into a DomainEvent.Payload. Uses the aggregate's own Snapshot() so the
// payload shape stays in lockstep with what the producer actually emits.
func snapshotPayload(t *testing.T) ([]byte, order.ID) {
	t.Helper()
	in := order.NewOrderInput{
		TenantID:      order.TenantID(fixedTenantID),
		LocationID:    order.LocationID(fixedLocationID),
		Source:        order.SourceOnline,
		SourceEventID: "test-1",
		Currency:      "USD",
		PlacedAt:      time.Now().UTC(),
		Items: []order.LineItem{{
			ID: uuid.Must(uuid.NewV7()), Position: 0, Name: "x", Quantity: 1,
			UnitPrice: 100, LineTotal: 100,
		}},
		Money:      order.MoneyBreakdown{Subtotal: 100, Total: 100},
		RawPayload: []byte("{}"),
	}
	o, err := order.NewOrder(in)
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	raw, err := json.Marshal(o.Snapshot())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	return raw, o.ID()
}

func TestSSE_SkipsNonReceivedEvents(t *testing.T) {
	pub := &fakePublisher{subscribers: 5}
	h := NewSSE(pub, zerolog.Nop())

	ev := order.DomainEvent{Type: order.EventOrderStatusChanged, AggregateID: order.ID(uuid.Must(uuid.NewV7()))}
	if err := h.Handle(context.Background(), ev); err != nil {
		t.Errorf("Handle returned %v; want nil for unrelated event type", err)
	}
	if pub.publishCalled {
		t.Error("Publish should not have been called for non-OrderReceived event")
	}
}

func TestSSE_TransientWhenNoSubscribers(t *testing.T) {
	payload, aggID := snapshotPayload(t)
	pub := &fakePublisher{subscribers: 0}
	h := NewSSE(pub, zerolog.Nop())

	ev := order.DomainEvent{Type: order.EventOrderReceived, AggregateID: aggID, Payload: payload}
	err := h.Handle(context.Background(), ev)
	if err == nil {
		t.Fatal("expected error when no subscribers")
	}
	if !errors.Is(err, dispatch.ErrTransient) {
		t.Errorf("err = %v; want wrapping dispatch.ErrTransient", err)
	}
	if pub.publishCalled {
		t.Error("should not call Publish when subscribers=0")
	}
}

func TestSSE_MalformedPayloadIsPermanent(t *testing.T) {
	pub := &fakePublisher{subscribers: 1}
	h := NewSSE(pub, zerolog.Nop())

	ev := order.DomainEvent{
		Type:        order.EventOrderReceived,
		AggregateID: order.ID(uuid.Must(uuid.NewV7())),
		Payload:     []byte(`{not json`),
	}
	err := h.Handle(context.Background(), ev)
	if err == nil {
		t.Fatal("expected error when payload is malformed")
	}
	if errors.Is(err, dispatch.ErrTransient) {
		t.Errorf("err = %v; should NOT wrap dispatch.ErrTransient (decode failures are permanent)", err)
	}
}

func TestSSE_HappyPath(t *testing.T) {
	payload, aggID := snapshotPayload(t)
	pub := &fakePublisher{subscribers: 2, delivered: 2}
	h := NewSSE(pub, zerolog.Nop())

	ev := order.DomainEvent{Type: order.EventOrderReceived, AggregateID: aggID, Payload: payload}
	if err := h.Handle(context.Background(), ev); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !pub.publishCalled {
		t.Fatal("Publish was not called on happy path")
	}
	// The bytes pushed to the broker should round-trip back to a Snapshot
	// matching the aggregate's id — proves the fat-payload path works
	// end-to-end without a DB read.
	var got map[string]any
	if err := json.Unmarshal(pub.lastBytes, &got); err != nil {
		t.Fatalf("re-decode wire bytes: %v", err)
	}
	if got["id"] != uuid.UUID(aggID).String() {
		t.Errorf("wire id = %v, want %v", got["id"], aggID)
	}
}

func TestSSE_RaceToZeroSubscribersIsTransient(t *testing.T) {
	payload, aggID := snapshotPayload(t)
	pub := &fakePublisher{subscribers: 1, delivered: 0} // raced
	h := NewSSE(pub, zerolog.Nop())

	ev := order.DomainEvent{Type: order.EventOrderReceived, AggregateID: aggID, Payload: payload}
	err := h.Handle(context.Background(), ev)
	if err == nil {
		t.Fatal("expected error when delivered=0")
	}
	if !errors.Is(err, dispatch.ErrTransient) {
		t.Errorf("err = %v; want wrapping dispatch.ErrTransient", err)
	}
}

func TestSSE_NameIsStable(t *testing.T) {
	if got := (&SSE{}).Name(); got != HandlerNameSSE {
		t.Errorf("Name() = %q; want %q", got, HandlerNameSSE)
	}
}
