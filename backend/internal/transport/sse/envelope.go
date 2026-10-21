// Package sse owns the wire shape of order events as they cross the SSE
// boundary from main/ to the agent. The body of the wire shape is
// order.Snapshot — the same shape the outbox stores and the HTTP read
// endpoints emit. This package adds only the trace-context fields the
// agent needs to chain its spans into main/'s trace.
package sse

import (
	"context"

	"quiccpos/main/internal/domain/order"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// OrderEvent is what an agent reads off the SSE stream. The Snapshot is
// embedded so the JSON shape is flat (no `snapshot:` wrapper key) — the
// agent sees one object with order fields plus optional `_traceparent` /
// `_tracestate` for trace continuity.
type OrderEvent struct {
	order.Snapshot

	Traceparent string `json:"_traceparent,omitempty"`
	Tracestate  string `json:"_tracestate,omitempty"`
}

// EventFromSnapshot wraps a Snapshot with the current trace context. Used
// by both the SSE dispatch handler (which has already deserialized the
// snapshot from the outbox payload) and by HTTP read endpoints (which can
// pass an empty trace context — the omitempty tags drop the fields).
func EventFromSnapshot(ctx context.Context, snap order.Snapshot, includeTrace bool) OrderEvent {
	ev := OrderEvent{Snapshot: snap}
	if includeTrace {
		carrier := propagation.MapCarrier{}
		otel.GetTextMapPropagator().Inject(ctx, carrier)
		ev.Traceparent = carrier["traceparent"]
		ev.Tracestate = carrier["tracestate"]
	}
	return ev
}
