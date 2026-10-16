// Package sse is the live fan-out layer for SSE subscribers. Stateless and
// in-memory: it holds a set of subscriber channels keyed by (tenant_id,
// location_id) so an agent only sees events for its own scope. Marshaling
// and trace-context injection are the caller's job (the dispatch SSE
// handler) — the broker is just a non-blocking, mutex-guarded fan-out
// primitive.
//
// Durability and replay live in the outbox/dispatcher pair (see
// internal/dispatch). On Subscribe we call Wake() so any backlog the new
// client missed is delivered without waiting for a normal poll tick.
package sse

import (
	"context"
	"sync"

	"quiccpos/main/internal/observability"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "quiccpos/main/sse"

// SSE event names carried in a Frame. `order` is the domain event stream;
// `command` carries a duplex RPC envelope (see internal/rpc).
const (
	EventOrder   = "order"
	EventCommand = "command"
	// EventStoreConfig is the store context (tenant + location + printers).
	// Sent as the first frame on every connect, and pushed mid-stream whenever
	// the manager edits the location or printers — the agent applies it live.
	EventStoreConfig = "store_config"
)

// Frame is one SSE message: an event name + its pre-marshaled data. The broker
// fans out Frames so a single scoped stream can multiplex order events and
// command frames without a second connection.
type Frame struct {
	Event string
	Data  []byte
}

// Scope identifies which event stream a subscriber is interested in. Two
// scopes never share a subscriber set — cross-tenant fan-out is impossible
// by construction.
type Scope struct {
	TenantID   uuid.UUID
	LocationID uuid.UUID
}

// Waker is what the broker calls on Subscribe so the dispatcher drains
// pending events immediately for the new client.
type Waker interface {
	Wake()
}

// ScopeWaker is an optional Waker upgrade: on Subscribe the broker calls
// WakeScope instead of Wake so the dispatcher can clear that scope's retry
// backoff before draining. The dispatcher implements it.
type ScopeWaker interface {
	WakeScope(ctx context.Context, tenantID, locationID uuid.UUID)
}

type Broker struct {
	mu      sync.Mutex
	clients map[Scope]map[chan Frame]struct{}
	waker   Waker
	logger  zerolog.Logger
	meters  observability.Meters
}

func New(lg zerolog.Logger, meters observability.Meters) *Broker {
	return &Broker{
		clients: make(map[Scope]map[chan Frame]struct{}),
		logger:  lg.With().Str("module", "sse-broker").Logger(),
		meters:  meters,
	}
}

func (b *Broker) SetWaker(w Waker) { b.waker = w }

// Subscribe registers a channel for the given scope and returns it plus an
// unsubscribe func. The dispatcher is woken on subscribe so the new client
// gets any pending events for its scope within one tick.
func (b *Broker) Subscribe(ctx context.Context, scope Scope) (chan Frame, func()) {
	ch := make(chan Frame, 8)

	b.mu.Lock()
	set, ok := b.clients[scope]
	if !ok {
		set = make(map[chan Frame]struct{})
		b.clients[scope] = set
	}
	set[ch] = struct{}{}
	b.mu.Unlock()

	if b.waker != nil {
		// Prefer a scope-aware wake (the dispatcher clears this scope's
		// backoff first so a reconnecting agent's backlog drains now).
		if sw, ok := b.waker.(ScopeWaker); ok {
			sw.WakeScope(ctx, scope.TenantID, scope.LocationID)
		} else {
			b.waker.Wake()
		}
	}

	return ch, func() {
		b.mu.Lock()
		if set, ok := b.clients[scope]; ok {
			if _, ok := set[ch]; ok {
				delete(set, ch)
				close(ch)
			}
			if len(set) == 0 {
				delete(b.clients, scope)
			}
		}
		b.mu.Unlock()
	}
}

// SubscribersFor returns how many clients are currently subscribed to a
// specific scope. The SSE dispatch handler uses this to skip publish work
// when nobody on the receiving end cares.
func (b *Broker) SubscribersFor(scope Scope) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.clients[scope])
}

// Publish pushes a Frame to every subscriber of the given scope. Returns the
// count delivered.
func (b *Broker) Publish(ctx context.Context, scope Scope, frame Frame) int {
	tracer := otel.Tracer(tracerName)
	ctx, span := tracer.Start(ctx, "sse.publish",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("tenant.id", scope.TenantID.String()),
			attribute.String("location.id", scope.LocationID.String()),
			attribute.String("sse.event", frame.Event),
		),
	)
	defer span.End()

	delivered := 0
	b.mu.Lock()
	set := b.clients[scope]
	subs := len(set)
	for ch := range set {
		select {
		case ch <- frame:
			delivered++
		default:
			b.logger.Warn().Ctx(ctx).Msg("SSE subscriber channel full; dropping payload for them")
		}
	}
	b.mu.Unlock()

	span.SetAttributes(
		attribute.Int("sse.subscribers", subs),
		attribute.Int("sse.delivered", delivered),
	)

	// Broadcast metric: event_type is order|command (the only Frame events
	// that flow through the broker); result reflects whether anyone was
	// listening on this scope.
	result := "no_subscribers"
	if subs > 0 {
		result = "delivered"
	}
	b.meters.SSEEventsBroadcast.Add(ctx, 1, metric.WithAttributes(
		attribute.String("event_type", frame.Event),
		attribute.String("result", result),
	))
	return delivered
}

// PublishCommand fans out a duplex RPC command frame (main→agent). Satisfies
// rpc.CommandPublisher.
func (b *Broker) PublishCommand(ctx context.Context, scope Scope, data []byte) int {
	return b.Publish(ctx, scope, Frame{Event: EventCommand, Data: data})
}
