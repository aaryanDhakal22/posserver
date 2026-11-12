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
	"time"

	"quiccpos/main/internal/observability"
	"quiccpos/main/internal/shared/safego"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
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

// subscriberBuffer is each stream's queue. Generous so a reconnect backlog or a
// burst never overflows it; an overflow closes that stream (Publish) so the
// agent reconnects and catches up — never a silently dropped frame.
const subscriberBuffer = 256

type Broker struct {
	mu      sync.Mutex
	clients map[Scope]map[chan Frame]struct{}
	// devices maps each stream to the device that opened it (uuid.Nil when
	// unknown), so a revoked agent's live stream can be cut immediately.
	devices map[chan Frame]uuid.UUID
	waker   Waker
	logger  zerolog.Logger
	meters  observability.Meters
	// onSubscribe hooks run (off the caller's path) whenever a stream opens.
	onSubscribe []func(scope Scope)
	// left is when each scope's last stream closed (Presence: how long its Pi
	// has been away); started is when this broker began (a scope never seen).
	left    map[Scope]time.Time
	started time.Time
}

// OnSubscribe registers a hook called (in its own goroutine) whenever a
// stream subscribes to a scope — e.g. the geo worker re-pushes what the
// (re)connected agent has not acked. Wire before serving.
func (b *Broker) OnSubscribe(fn func(scope Scope)) { b.onSubscribe = append(b.onSubscribe, fn) }

func New(lg zerolog.Logger, meters observability.Meters) *Broker {
	return &Broker{
		clients: make(map[Scope]map[chan Frame]struct{}),
		devices: make(map[chan Frame]uuid.UUID),
		left:    make(map[Scope]time.Time),
		started: time.Now(),
		logger:  lg.With().Str("module", "sse-broker").Logger(),
		meters:  meters,
	}
}

func (b *Broker) SetWaker(w Waker) { b.waker = w }

// Subscribe registers a channel for the given scope and returns it plus an
// unsubscribe func. The dispatcher is woken on subscribe so the new client
// gets any pending events for its scope within one tick.
func (b *Broker) Subscribe(ctx context.Context, scope Scope) (chan Frame, func()) {
	return b.SubscribeAs(ctx, scope, uuid.Nil)
}

// SubscribeAs is Subscribe for a known device (see DisconnectDevice).
func (b *Broker) SubscribeAs(ctx context.Context, scope Scope, deviceID uuid.UUID) (chan Frame, func()) {
	ch := make(chan Frame, subscriberBuffer)

	b.mu.Lock()
	set, ok := b.clients[scope]
	if !ok {
		set = make(map[chan Frame]struct{})
		b.clients[scope] = set
	}
	set[ch] = struct{}{}
	b.devices[ch] = deviceID
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
	for _, fn := range b.onSubscribe {
		safego.Go(context.WithoutCancel(ctx), "sse.on_subscribe", func(context.Context) { fn(scope) })
	}

	return ch, func() {
		b.mu.Lock()
		b.removeLocked(scope, ch)
		b.mu.Unlock()
	}
}

// removeLocked drops one stream (closing its channel once). Caller holds mu.
func (b *Broker) removeLocked(scope Scope, ch chan Frame) {
	if set, ok := b.clients[scope]; ok {
		if _, ok := set[ch]; ok {
			delete(set, ch)
			delete(b.devices, ch)
			close(ch)
		}
		if len(set) == 0 {
			delete(b.clients, scope)
			b.left[scope] = time.Now()
		}
	}
}

// Presence is a scope's live streams and, when there are none, since when
// (its last stream closed, else when this broker started).
func (b *Broker) Presence(scope Scope) (streams int, awaySince time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n := len(b.clients[scope]); n > 0 {
		return n, time.Time{}
	}
	if t, ok := b.left[scope]; ok {
		return 0, t
	}
	return 0, b.started
}

// DisconnectDevice closes every live stream opened by deviceID (a revoked
// agent): its handler returns, the agent reconnects, gets 401 and wipes
// itself. Returns how many streams were closed.
func (b *Broker) DisconnectDevice(deviceID uuid.UUID) int {
	if deviceID == uuid.Nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for scope, set := range b.clients {
		for ch := range set {
			if b.devices[ch] == deviceID {
				b.removeLocked(scope, ch)
				n++
			}
		}
	}
	return n
}

// Scopes lists the scopes with at least one subscriber now.
func (b *Broker) Scopes() []Scope {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Scope, 0, len(b.clients))
	for sc, set := range b.clients {
		if len(set) > 0 {
			out = append(out, sc)
		}
	}
	return out
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

	delivered, cut := 0, 0
	b.mu.Lock()
	set := b.clients[scope]
	subs := len(set)
	for ch := range set {
		select {
		case ch <- frame:
			delivered++
		default:
			// A stream whose queue is full is too slow (or stalled): dropping
			// the frame would lose a command or store_config for good. Cut the
			// stream instead (its handler returns, the agent reconnects and
			// catches up: unacked orders are re-sent, the handshake carries
			// the store_config) — HARDENING-PLAN R-3.
			dev := b.devices[ch]
			b.removeLocked(scope, ch)
			cut++
			b.logger.Warn().Ctx(ctx).Str("device_id", dev.String()).Str("sse_event", frame.Event).
				Msg("SSE subscriber too slow (queue full); stream closed so it reconnects")
		}
	}
	b.mu.Unlock()
	if cut > 0 {
		span.SetAttributes(attribute.Int("sse.cut", cut))
	}

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
	b.meters.SSEEventsBroadcast.Add(ctx, 1, observability.WithStore(ctx, scope.TenantID,
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
