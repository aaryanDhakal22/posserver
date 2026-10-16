// Package dispatch is the back half of the transactional outbox pattern.
//
//	[order.Order aggregate].PullEvents()
//	      │
//	      ▼ (in same tx as state)
//	      outbox_events + outbox_dispatches
//	      │
//	      ▼ (poll loop)
//	      Dispatcher.run()
//	      │  for each pending event
//	      │  for each handler
//	      ▼
//	      Handler.Handle(ctx, event)
//	      │
//	      ▼ success → MarkDispatched ; failure → MarkFailed (with backoff)
//
// Handlers are pluggable: today the only one is the SSE adapter, soon there
// will be push-notification, audit-archival, and per-tenant fan-out handlers.
// Adding a handler is additive — existing dispatched events are not
// re-delivered.
package dispatch

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"quiccpos/main/internal/domain/order"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "quiccpos/main/dispatch"

// ErrTransient signals a handler couldn't deliver right now but a retry
// might succeed (e.g. SSE handler with 0 subscribers). The dispatcher backs
// off and retries; the dispatched_at timestamp stays NULL.
//
// Wrap with %w to add context: fmt.Errorf("no subscribers: %w", ErrTransient)
var ErrTransient = errors.New("dispatch: transient handler failure")

// Handler is one consumer of domain events. Implementations should:
//   - return nil  on success (dispatcher marks dispatched_at = NOW()).
//   - return an error wrapping ErrTransient for retryable failures.
//   - return any other error for permanent failures (still retried with
//     backoff today; will be DLQ'd in a future iteration).
//
// Handle MUST be idempotent — the dispatcher may call it more than once for
// the same event under crash/retry conditions.
type Handler interface {
	Name() string
	Handle(ctx context.Context, event order.DomainEvent) error
}

// PendingEvent is an outbox event awaiting one handler, plus how many times
// that handler has already tried it (outbox_dispatches.attempts) — the input
// to the per-row exponential backoff.
type PendingEvent struct {
	Event    order.DomainEvent
	Attempts int
}

// OutboxRepo is the slice of OutboxRepository the dispatcher uses.
type OutboxRepo interface {
	PendingEventsForHandler(ctx context.Context, handlerName string, limit int) ([]PendingEvent, error)
	MarkDispatched(ctx context.Context, eventID uuid.UUID, handlerName string) error
	MarkFailed(ctx context.Context, eventID uuid.UUID, handlerName string, errMsg string, nextAttempt time.Time) error
	// ResetBackoffForScope clears next_attempt_at on every still-pending
	// dispatch row for one (tenant, location), so backed-off events become
	// due immediately (used when that scope's agent reconnects).
	ResetBackoffForScope(ctx context.Context, tenantID, locationID uuid.UUID) error
}

// Dispatcher polls the outbox and fans events out to handlers. One
// dispatcher serves all handlers; each tick processes a small batch per
// handler so a slow handler can't starve the others.
type Dispatcher struct {
	repo         OutboxRepo
	handlers     []Handler
	pollInterval time.Duration
	batchSize    int
	logger       zerolog.Logger
	tracer       trace.Tracer

	wake chan struct{}

	// metrics — optional; nil-safe.
	hooks Hooks
}

// Hooks lets the bootstrap report dispatch-rate and depth metrics without
// the dispatcher knowing about the meters package. Every method is called
// outside the handler critical path. Pass a zero Hooks to disable.
type Hooks struct {
	OnEventDispatched func(handler string, eventType order.EventType)
	OnEventFailed     func(handler string, eventType order.EventType, transient bool)
}

type Option func(*Dispatcher)

func WithPollInterval(d time.Duration) Option { return func(x *Dispatcher) { x.pollInterval = d } }
func WithBatchSize(n int) Option              { return func(x *Dispatcher) { x.batchSize = n } }
func WithHooks(h Hooks) Option                { return func(x *Dispatcher) { x.hooks = h } }

func New(repo OutboxRepo, handlers []Handler, lg zerolog.Logger, opts ...Option) *Dispatcher {
	d := &Dispatcher{
		repo:         repo,
		handlers:     handlers,
		pollInterval: 1 * time.Second,
		batchSize:    50,
		logger:       lg.With().Str("module", "dispatcher").Logger(),
		tracer:       otel.Tracer(tracerName),
		wake:         make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// HandlerNames returns the registered handler names in registration order.
// The repo uses this at startup to write one outbox_dispatches row per
// handler when an event is emitted.
func (d *Dispatcher) HandlerNames() []string {
	names := make([]string, 0, len(d.handlers))
	for _, h := range d.handlers {
		names = append(names, h.Name())
	}
	return names
}

// Wake nudges the dispatcher to run an immediate poll (instead of waiting
// for the next tick). Call this when you know there's work to do — e.g. an
// SSE subscriber just connected and there may be queued events for them.
// Non-blocking; coalesces multiple Wakes into one tick.
func (d *Dispatcher) Wake() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Run blocks until ctx is cancelled. Polls every pollInterval AND on Wake.
// Process is sequential within a tick (handlers run in registration order)
// — keeps it easy to reason about and avoids handlers stomping on each
// other's transactions. Per-handler latency is bounded by batchSize × per-
// event handle time.
func (d *Dispatcher) Run(ctx context.Context) {
	d.logger.Info().
		Int("handlers", len(d.handlers)).
		Strs("handler_names", d.HandlerNames()).
		Dur("poll_interval", d.pollInterval).
		Msg("dispatcher started")

	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()

	for {
		// Process once unconditionally on entry so events emitted just
		// before Run started don't have to wait for the first tick.
		d.tick(ctx)

		select {
		case <-ctx.Done():
			d.logger.Info().Msg("dispatcher stopped")
			return
		case <-ticker.C:
		case <-d.wake:
		}
	}
}

func (d *Dispatcher) tick(ctx context.Context) {
	for _, h := range d.handlers {
		d.runHandler(ctx, h)
	}
}

func (d *Dispatcher) runHandler(ctx context.Context, h Handler) {
	events, err := d.repo.PendingEventsForHandler(ctx, h.Name(), d.batchSize)
	if err != nil {
		d.logger.Warn().Err(err).Str("handler", h.Name()).Msg("PendingEventsForHandler failed")
		return
	}
	if len(events) == 0 {
		return
	}

	d.logger.Debug().Str("handler", h.Name()).Int("count", len(events)).Msg("dispatching batch")

	for _, pe := range events {
		d.dispatchOne(ctx, h, pe)
	}
}

// dispatchOne wraps a single Handle call in a span and routes the result to
// MarkDispatched or MarkFailed.
func (d *Dispatcher) dispatchOne(ctx context.Context, h Handler, pe PendingEvent) {
	ev := pe.Event
	ctx, span := d.tracer.Start(ctx, "dispatch.handle",
		trace.WithAttributes(
			attribute.String("handler", h.Name()),
			attribute.String("event.id", ev.ID.String()),
			attribute.String("event.type", string(ev.Type)),
			attribute.String("event.aggregate_id", uuid.UUID(ev.AggregateID).String()),
			attribute.Int("dispatch.attempts", pe.Attempts),
		),
	)
	defer span.End()

	err := h.Handle(ctx, ev)
	if err == nil {
		if mErr := d.repo.MarkDispatched(ctx, ev.ID, h.Name()); mErr != nil {
			d.logger.Warn().Err(mErr).Str("handler", h.Name()).Str("event_id", ev.ID.String()).
				Msg("dispatch succeeded but MarkDispatched failed; will retry as duplicate")
			span.RecordError(mErr)
			return
		}
		if d.hooks.OnEventDispatched != nil {
			d.hooks.OnEventDispatched(h.Name(), ev.Type)
		}
		return
	}

	transient := errors.Is(err, ErrTransient)
	span.RecordError(err)
	span.SetStatus(codes.Error, classifyError(transient))

	level := d.logger.Warn()
	if transient {
		level = d.logger.Debug()
	}
	level.Err(err).
		Str("handler", h.Name()).
		Str("event_id", ev.ID.String()).
		Bool("transient", transient).
		Int("attempts", pe.Attempts+1).
		Msg("handler returned error")

	next := time.Now().Add(backoffDelay(pe.Attempts, transient, rand.Int64N))
	if mErr := d.repo.MarkFailed(ctx, ev.ID, h.Name(), err.Error(), next); mErr != nil {
		d.logger.Warn().Err(mErr).Str("handler", h.Name()).Str("event_id", ev.ID.String()).
			Msg("MarkFailed failed; dispatch row will be retried but state is stale")
	}
	if d.hooks.OnEventFailed != nil {
		d.hooks.OnEventFailed(h.Name(), ev.Type, transient)
	}
}

func classifyError(transient bool) string {
	if transient {
		return "transient"
	}
	return "permanent"
}

// Per-row exponential backoff bounds. Transient failures (e.g. the SSE handler
// with no agent connected) start fast and cap low so a returning agent is
// caught up quickly even without a scope reset; permanent failures back off
// hard so a buggy handler doesn't churn the DB.
const (
	transientBackoffBase = 1 * time.Second
	transientBackoffCap  = 30 * time.Second
	permanentBackoffBase = 5 * time.Second
	permanentBackoffCap  = 10 * time.Minute
)

// backoffDelay returns how long to wait before retrying a dispatch that has
// already been attempted `attempts` times (this failure not included):
// base·2^attempts capped, with "equal jitter" — half fixed, half random — so
// retries spread out but never collapse to an immediate re-poll. randN(n)
// returns a uniform int64 in [0, n) (rand.Int64N; injectable for tests).
func backoffDelay(attempts int, transient bool, randN func(int64) int64) time.Duration {
	base, maxDelay := permanentBackoffBase, permanentBackoffCap
	if transient {
		base, maxDelay = transientBackoffBase, transientBackoffCap
	}
	d := maxDelay
	if attempts < 32 { // beyond this the shift overflows; the cap applies anyway
		if exp := base << uint(attempts); exp > 0 && exp < maxDelay {
			d = exp
		}
	}
	half := d / 2
	return half + time.Duration(randN(int64(d-half)+1))
}

// WakeScope is what the SSE broker calls when an agent subscribes: it clears
// the backoff on that scope's pending events (they may be minutes out after a
// long outage) and then wakes the dispatcher so the backlog drains within one
// tick. A reset failure is logged and the Wake still happens.
func (d *Dispatcher) WakeScope(ctx context.Context, tenantID, locationID uuid.UUID) {
	if err := d.repo.ResetBackoffForScope(ctx, tenantID, locationID); err != nil {
		d.logger.Warn().Err(err).Str("tenant_id", tenantID.String()).Str("location_id", locationID.String()).
			Msg("ResetBackoffForScope failed; backlog drains on its normal schedule")
	}
	d.Wake()
}
