# How `main/` works — order ingest, outbox, dispatcher, SSE

This is the internal-design doc for the order pipeline in `main/`. It explains
the four layers that move an order from SQS to a connected agent, how they
fit together, what the failure semantics are, and which race conditions are
actively prevented vs. deliberately tolerated.

> Audience: someone modifying any file under `internal/infra/sse`,
> `internal/dispatch`, `internal/app/order`, or the SQS consumer.

---

## Big picture

```
                 ┌────────────────────────────────────────────────────┐
                 │                     SQS message                    │
                 │  MessageAttributes: source = online | mobile | …   │
                 │  Body: bare vendor JSON                            │
                 └───────────────────────┬────────────────────────────┘
                                         ▼
                            ┌───────────────────────┐
                            │   sqs.Consumer        │   reads `source` attr,
                            │   processMessage()    │   routes to caster
                            └───────────┬───────────┘
                                        ▼
                            ┌───────────────────────┐
                            │ casting/online.Caster │   raw bytes → aggregate
                            └───────────┬───────────┘
                                        ▼
                            ┌───────────────────────┐
                            │     Service.Create    │   persist + Wake()
                            └───────────┬───────────┘
                                        ▼
                ┌──────────────────────────────────────────────┐
                │  OrderRepository.Create                       │
                │  ─────────────────────────────                │
                │  ONE TRANSACTION:                             │
                │   INSERT orders                               │
                │   INSERT line_items (recursive)               │
                │   INSERT payments                             │
                │   INSERT status_events                        │
                │   INSERT outbox_events                        │
                │   INSERT outbox_dispatches (one per handler)  │
                └──────────────────────┬────────────────────────┘
                                       ▼
                ┌──────────────────────────────────────────────┐
                │  Dispatcher (one goroutine, polls + Wake)    │
                │   for each pending event:                    │
                │     for each handler:                        │
                │       Handler.Handle(ctx, ev)                │
                │       → MarkDispatched / MarkFailed          │
                └──────────────────────┬────────────────────────┘
                                       ▼
                            ┌───────────────────────┐
                            │   handlers.SSE        │   load aggregate,
                            │   (one of N handlers) │   call broker
                            └───────────┬───────────┘
                                        ▼
                            ┌───────────────────────┐
                            │   sse.Broker          │   in-memory fan-out
                            │   (stateless)         │   to current channels
                            └───────────┬───────────┘
                                        ▼
                            ┌───────────────────────┐
                            │ HTTP SSEHandler       │   reads channel, writes
                            │ StreamOrders()        │   `event: order\n…`
                            └───────────────────────┘
```

The transactional outbox sits between persist and side-effects so an order is
never half-saved (state without event, or event without state). Adding a new
side effect (push notifications, audit, anything) is one new
`dispatch.Handler` and one entry in the bootstrap registry — no changes to
the persist path.

---

## Layer 1 — the SSE broker (`internal/infra/sse/broker.go`)

The broker is **stateless live fan-out**. No DB, no goroutines, no durability.
Its only job is "deliver these bytes to whichever subscribers are connected
*right now*."

### Shape

```go
type Broker struct {
    mu      sync.Mutex
    clients map[chan []byte]struct{}
    waker   Waker
    logger  zerolog.Logger
}
```

`Waker` is a one-method interface (`Wake()`) so the broker can poke the
dispatcher without importing it (avoids a cycle):

```go
type Waker interface {
    Wake()
}
```

The bootstrap calls `broker.SetWaker(dispatcher)` once both are constructed.

### Subscribe

```go
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
        // The dispatcher implements the optional ScopeWaker: it clears this
        // scope's retry backoff (ResetDispatchBackoffForScope) and then Wakes.
        if sw, ok := b.waker.(ScopeWaker); ok {
            sw.WakeScope(ctx, scope.TenantID, scope.LocationID)
        } else {
            b.waker.Wake()
        }
    }

    return ch, func() { /* delete from b.clients[scope] + close(ch), double-unsub safe */ }
}
```

Notes:
- The per-subscriber channel is buffered to **8**. Big enough to absorb a
  brief consumer hiccup, small enough that a truly stalled subscriber gets
  noticed (their drops light up `sse_test`-style logs).
- `Wake()` (via `WakeScope`) is fired **after** the subscriber is in `clients`,
  so the dispatcher's drain on the next tick will find the channel ready to
  receive. `WakeScope` first resets `next_attempt_at` for the scope's pending
  rows — without it, a backlog that backed off while the agent was away could
  wait up to the 30s transient cap.
- The unsub closure re-checks `clients[ch]` before `delete` + `close` so
  double-unsub is a no-op (no `close of closed channel` panic).

### Publish

```go
func (b *Broker) Publish(ctx context.Context, data []byte) int {
    // … OTEL span elided …
    delivered := 0
    b.mu.Lock()
    for ch := range b.clients {
        select {
        case ch <- data:
            delivered++
        default:
            b.logger.Warn().Msg("SSE subscriber channel full; dropping payload for them")
        }
    }
    b.mu.Unlock()
    return delivered
}
```

Notes:
- Takes pre-marshaled bytes — the broker doesn't know about Snapshot,
  OrderEvent, or anything order-shaped. Pure fan-out primitive.
- Returns `delivered` — the count is what the SSE handler uses to detect
  "raced to zero subscribers" (race #3 below).
- Send is **non-blocking** (`select { case ch <- data: default: }`). A
  stalled agent can never block the publish path. The cost is that one event
  may not reach that agent; they recover via the agent's backfill endpoint.

---

## Layer 2 — the SSE dispatch handler (`internal/dispatch/handlers/sse.go`)

The broker speaks its own native interface; the dispatcher only knows
`dispatch.Handler`. The SSE handler bridges the two.

### Handler interface (in `internal/dispatch/dispatcher.go`)

```go
type Handler interface {
    Name() string
    Handle(ctx context.Context, event order.DomainEvent) error
}
```

`Name()` is a stable on-disk identifier — it's the primary key in
`outbox_dispatches`. Renaming it strands historical events undelivered.

### The SSE handler

```go
const HandlerNameSSE = "sse-broadcast"

func (h *SSE) Handle(ctx context.Context, ev order.DomainEvent) error {
    // Today only OrderReceived drives an SSE push. Skipping unknown types
    // counts as a successful dispatch — the row is marked dispatched.
    if ev.Type != order.EventOrderReceived {
        return nil
    }

    // Fast-path: no subscribers means nothing to do. Returning ErrTransient
    // leaves the row pending; the next subscriber's Wake() (or the next
    // tick) will retry.
    if h.publisher.Subscribers() == 0 {
        return fmt.Errorf("no SSE subscribers connected: %w", dispatch.ErrTransient)
    }

    // Fat-payload contract: the snapshot IS the payload. Decode + wrap
    // with trace context + push. No DB read; no chance the aggregate has
    // mutated since this event was emitted.
    var snap order.Snapshot
    if err := json.Unmarshal(ev.Payload, &snap); err != nil {
        return fmt.Errorf("decode snapshot from outbox payload: %w", err)
    }

    wireEv := sseTransport.EventFromSnapshot(ctx, snap, true)
    data, err := json.Marshal(wireEv)
    if err != nil {
        return fmt.Errorf("marshal SSE event: %w", err)
    }

    delivered := h.publisher.Publish(ctx, data)
    if delivered == 0 {
        // Race: subscribers were >0 at the check, then all disconnected
        // before we sent. Retry transiently.
        return fmt.Errorf("publish raced to zero subscribers: %w", dispatch.ErrTransient)
    }
    return nil
}
```

`dispatch.ErrTransient` is a sentinel: wrapping it tells the dispatcher
"don't mark dispatched; come back to me." Anything else is treated as
permanent (still retried with backoff today; will be DLQ'd in a future
iteration).

---

## Layer 3 — the dispatcher (`internal/dispatch/dispatcher.go`)

One goroutine. Polls every `pollInterval` (default 1 s) **or** when `Wake()`
fires. Per tick, walks every registered handler and processes its pending
batch.

### Wake — the coalescing nudge

```go
type Dispatcher struct {
    // …
    wake chan struct{}     // capacity 1
}

func (d *Dispatcher) Wake() {
    select {
    case d.wake <- struct{}{}:
    default:
    }
}
```

Capacity-1 buffered channel + non-blocking send means:
- Multiple `Wake()` calls coalesce into at most one pending wake-up.
- The caller never blocks.
- Lost wake-ups are fine — the next tick still runs unconditionally.

### Run loop

```go
func (d *Dispatcher) Run(ctx context.Context) {
    ticker := time.NewTicker(d.pollInterval)
    defer ticker.Stop()

    for {
        // Process once unconditionally on entry so events emitted just
        // before Run started don't have to wait for the first tick.
        d.tick(ctx)

        select {
        case <-ctx.Done():
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
```

Sequential within a tick — handlers run in registration order. Keeps it
trivial to reason about and avoids handlers stomping on each other's DB
state.

### Per-event dispatch

```go
func (d *Dispatcher) dispatchOne(ctx context.Context, h Handler, pe PendingEvent) {
    ev := pe.Event // PendingEvent{Event, Attempts} — attempts from outbox_dispatches
    // …span setup elided…
    err := h.Handle(ctx, ev)
    if err == nil {
        d.repo.MarkDispatched(ctx, ev.ID, h.Name())
        if d.hooks.OnEventDispatched != nil {
            d.hooks.OnEventDispatched(h.Name(), ev.Type)
        }
        return
    }

    transient := errors.Is(err, ErrTransient)
    next := time.Now().Add(backoffDelay(pe.Attempts, transient, rand.Int64N))
    d.repo.MarkFailed(ctx, ev.ID, h.Name(), err.Error(), next) // attempts++
    if d.hooks.OnEventFailed != nil {
        d.hooks.OnEventFailed(h.Name(), ev.Type, transient)
    }
}

// base·2^attempts, capped, "equal jitter" (half fixed, half random — never 0):
//   transient 1s → 30s cap   (e.g. SSE handler, no agent connected)
//   permanent 5s → 10min cap (a buggy handler)
func backoffDelay(attempts int, transient bool, randN func(int64) int64) time.Duration
```

`Hooks.OnEventDispatched` / `OnEventFailed` are how the bootstrap wires the
dispatcher into the OTEL meters without the dispatcher importing `metric`.

---

### Why fat payload?

The outbox `payload` carries the **full `order.Snapshot`** at event time —
not a thin `{aggregate_id}` pointer. The handler doesn't load the
aggregate from the DB; it deserializes the payload and pushes.

Three reasons:

1. **Snapshot semantics match the event's meaning.** `order.received` means
   "this is what arrived right now." If the order is later cancelled, the
   handler should still broadcast the *received* state — that's what the
   event says happened. A thin payload that re-loads the aggregate would
   broadcast the *current* state and lie about history. Same argument
   applies to `order.status_changed` — we want the post-transition
   snapshot, not whatever the order has become by handle time.

2. **Every consumer needs the whole order.** SSE → agent → printer needs
   it. Push notifications need customer + summary. Audit archival needs
   everything. There's no handler today (or imagined) that benefits from
   a thin payload's "small-and-load-on-demand" trade.

3. **One DB hit becomes zero.** No `loader.GetByID` on the dispatch path,
   no read-after-write replica races to worry about.

Cost is storage: ~2–5 KB per event vs ~50 bytes for a thin pointer. At
restaurant volumes (low thousands of orders/day) this is gigabytes per
year — trivial and easy to archive.

The price is **schema discipline**. The `outbox_events.schema_version`
field stops being decorative — it's how handlers know which payload type
to deserialize when we evolve `Snapshot`. Today only v1 exists. When we
change the shape, we bump it and handlers branch on the version.

## Layer 4 — the outbox (DB)

Two tables. Schema in `backend/sql/schema.sql`:

```sql
CREATE TABLE outbox_events (
    id              uuid        PRIMARY KEY,
    aggregate_id    uuid        NOT NULL,
    type            text        NOT NULL,
    payload         jsonb       NOT NULL DEFAULT '{}'::jsonb,
    occurred_at     timestamptz NOT NULL,
    schema_version  integer     NOT NULL DEFAULT 1
);

CREATE TABLE outbox_dispatches (
    event_id         uuid        NOT NULL REFERENCES outbox_events(id) ON DELETE CASCADE,
    handler_name     text        NOT NULL,
    dispatched_at    timestamptz,
    attempts         integer     NOT NULL DEFAULT 0,
    last_error       text        NOT NULL DEFAULT '',
    last_attempt_at  timestamptz,
    next_attempt_at  timestamptz,
    PRIMARY KEY (event_id, handler_name)
);
```

Per-handler tracking via `outbox_dispatches`. Adding a new handler later
does NOT cause replay of historical events: when an event is emitted, the
repo writes one dispatch row per **currently registered** handler. Handlers
introduced after the fact only see events emitted from that point on.

### Payload typing

The Go side has one struct per event type:

```go
// internal/domain/order/events.go
const (
    EventOrderReceived       EventType = "order.received"        // payload: order.Snapshot
    EventOrderStatusChanged  EventType = "order.status_changed"  // payload: order.StatusChangedPayload
)

type DomainEvent struct {
    ID            uuid.UUID
    AggregateID   ID
    Type          EventType
    Payload       json.RawMessage  // marshaled at recordEvent time, jsonb in DB
    OccurredAt    time.Time
    SchemaVersion int
}
```

Aggregate methods record events with typed payloads:

```go
// internal/domain/order/entity.go (NewOrder)
o.recordEvent(EventOrderReceived, o.Snapshot())

// internal/domain/order/entity.go (Transition)
o.recordEvent(EventOrderStatusChanged, StatusChangedPayload{
    From: string(from), To: string(to), Actor: actor, Reason: reason,
    Snapshot: o.Snapshot(),
})
```

`order.Snapshot` (in `internal/domain/order/snapshot.go`) is the canonical
JSON-serializable shape used by the outbox payload, the SSE wire format,
and the HTTP read endpoints — there is exactly one shape for "an order on
the wire." `transport/sse.OrderEvent` embeds it and adds optional
`_traceparent` / `_tracestate` fields.

### The pending query (`backend/sql/query.sql`)

```sql
-- name: PendingEventsForHandler :many
SELECT e.id, e.aggregate_id, e.type, e.payload,
       e.occurred_at, e.schema_version, d.attempts
FROM outbox_events e
JOIN outbox_dispatches d ON d.event_id = e.id
WHERE d.handler_name = $1
  AND d.dispatched_at IS NULL
  AND (d.next_attempt_at IS NULL OR d.next_attempt_at <= NOW())
ORDER BY e.occurred_at ASC, e.id ASC
LIMIT $2;
```

Ordered by `occurred_at ASC` so dispatches preserve causal order within a
handler. The partial index
`idx_outbox_dispatches_pending (handler_name, next_attempt_at NULLS FIRST) WHERE dispatched_at IS NULL`
keeps this query cheap regardless of total outbox volume.

### Atomic emit (`internal/infra/database/repositories/order_repo.go`)

```go
func (r *OrderRepository) Create(ctx context.Context, o *order.Order) error {
    tx, err := r.pool.Begin(ctx)
    if err != nil { return err }
    defer tx.Rollback(ctx)

    q := models.New(tx)

    if err := q.CreateOrder(ctx, toOrderRow(o)); err != nil {
        if isUniqueViolation(err) {
            return order.ErrDuplicate
        }
        return err
    }

    // … line_items, payments, status_events …

    // Drain DomainEvents into the outbox in the SAME transaction.
    for _, ev := range o.PullEvents() {
        if err := insertOutboxEvent(ctx, q, ev, r.handlerNames); err != nil {
            return err
        }
    }

    return tx.Commit(ctx)
}
```

If any step fails the entire transaction rolls back — the order vanishes
*and* its events vanish. There is no in-between state.

---

## End-to-end: a new order

```
T+0    SQS message arrives
       └→ Consumer reads `source` attr, default = online
       └→ Consumer.processMessage → online.Caster.Cast(raw)

T+1    Caster returns *order.Order with one pending DomainEvent
       (recorded by NewOrder: type=order.received)

T+2    Service.Create(o)
       └→ Repo.Create
            └→ BEGIN
            └→ INSERT orders, line_items, payments, status_events
            └→ INSERT outbox_events
            └→ INSERT outbox_dispatches(handler="sse-broadcast", dispatched_at=NULL)
            └→ COMMIT
       └→ dispatcher.Wake()    // non-blocking, coalescing

T+3    Dispatcher's `<-d.wake` fires → tick() → runHandler(SSE)
       └→ PendingEventsForHandler("sse-broadcast", 50) → 1 row
       └→ SSE.Handle(ev)
            └→ Subscribers() == 1 (the connected agent)
            └→ loader.GetByID(ev.AggregateID) → aggregate
            └→ broker.PublishOrder(o) → delivered = 1
       └→ MarkDispatched(eventID, "sse-broadcast")

T+4    HTTP loop in StreamOrders:
       └→ <-ch fires
       └→ fmt.Fprintf(w, "event: order\ndata: %s\n\n", data)
       └→ flusher.Flush()
       └→ agent receives the event
```

If the agent isn't connected at T+3:
- `Subscribers() == 0` → handler returns `ErrTransient`.
- Outbox row stays `dispatched_at = NULL`.
- Each retry backs off (1s, 2s, 4s … capped at 30s, jittered).
- When the agent connects later, `Subscribe()` calls `WakeScope()` — clears
  the scope's backoff, then wakes.
- Next tick (~immediate), the same row comes back from the pending query and
  delivery succeeds.

---

## End-to-end: an agent reconnects with backlog

```
T+0    Agent disconnects (network blip, restart, …)

T+0..N N orders arrive while no one is listening:
       Each one persists + outbox row + Handle returns ErrTransient.
       Outbox: N rows with dispatched_at = NULL.

T+M    Agent reconnects → SSEHandler.StreamOrders → broker.Subscribe
       └→ Subscribe registers channel, calls dispatcher.WakeScope()
          (next_attempt_at = NULL for the scope's pending rows, then Wake)

T+M+ε  Dispatcher tick → PendingEventsForHandler("sse-broadcast", 50)
       └→ Returns up to 50 rows in occurred_at ASC order
       └→ For each: SSE.Handle → broker.PublishOrder → MarkDispatched

T+…    HTTP loop drains the buffered channel, agent sees orders in order.
```

Cap: the dispatcher batch size (default 50). If the backlog exceeds that,
the dispatcher catches up across multiple ticks. Older missed orders the
agent didn't get this way come from the agent's own backfill GET endpoint.

---

## Race conditions

The interesting ones, with how each is handled.

### #1 — Subscribe insert vs concurrent Publish

Two goroutines: one calling `Subscribe`, another finishing a `PublishOrder`.

`sync.Mutex` on `clients` serializes them. Either:
- Subscribe wins → its channel is in the map before Publish iterates → it
  gets the event.
- Publish wins → Subscribe registers afterward → misses this event but
  triggers `Wake()`. The dispatcher's next tick replays anything still
  `dispatched_at IS NULL`.

Either way, no event lost.

### #2 — Slow subscriber (channel full)

Per-subscriber channel is buffered to 8. If the agent reads slowly:

```go
for ch := range b.clients {
    select {
    case ch <- data:
        delivered++
    default:
        b.logger.Warn().Msg("SSE subscriber channel full; dropping payload for them")
    }
}
```

Non-blocking send drops the payload **for that subscriber only**. The
publish path keeps moving — one stalled agent can't gum up the others or
the dispatcher.

Cost: that subscriber misses that event. They recover via the agent's
backfill.

### #3 — Subscriber count check vs actual delivery

Inside `SSE.Handle`:

```go
if h.publisher.Subscribers() == 0 {
    return ErrTransient   // skip the DB load
}
// … load aggregate …
delivered, _ := h.publisher.PublishOrder(...)
if delivered == 0 {
    return ErrTransient   // raced — everyone disconnected
}
```

The `Subscribers()` check is an optimization (avoid a DB hit when obviously
no one is listening). Correctness rests on `delivered == 0 → ErrTransient`.
A subscriber count of 5 at the check moment can still drop to 0 by the time
PublishOrder runs; the second check catches that and the row stays pending.

### #4 — Concurrent `Wake()` calls

```go
func (d *Dispatcher) Wake() {
    select {
    case d.wake <- struct{}{}:
    default:
    }
}
```

Channel capacity 1, send is non-blocking. Surplus calls are silently
dropped. The dispatcher only needs to be told "there might be work" once
per cycle; coalescing is the desired behaviour. (One tick processes ALL
pending rows, so collapsing 100 wake-ups into 1 doesn't lose work.)

### #5 — Double unsub

Echo handlers can panic; cleanup paths can fire twice in unusual unwinding
scenarios. The unsub closure protects:

```go
return ch, func() {
    b.mu.Lock()
    if _, ok := b.clients[ch]; ok {
        delete(b.clients, ch)
        close(ch)
    }
    b.mu.Unlock()
}
```

The `if _, ok := b.clients[ch]; ok` check makes the second call a no-op,
preventing the `close of closed channel` panic.

### #6 — Handler runs, MarkDispatched fails (DB hiccup, crash)

The handler delivered to the agent, but the dispatcher couldn't update
`outbox_dispatches`. The row is still `dispatched_at = NULL`. Next tick,
the dispatcher tries again. Handler runs again. Agent receives a duplicate.

This is **at-least-once** semantics. The contract is that handlers must be
idempotent. The agent satisfies this by deduplicating on aggregate `id` in
its 24-hour store.

### #7 — Persist commits but `Wake()` never fires (process crash)

`Service.Create` does `repo.Create` then `Wake()`. What if it crashes
between the two?

The outbox row is durably committed. The dispatcher's regular 1-second
poll picks it up after restart. `Wake()` is purely a latency optimization;
it isn't required for correctness.

### #8 — New subscriber connects after previous one received the event

`dispatched_at` flips after delivery to whoever was first. A subscriber
connecting later finds the dispatch row already marked dispatched, so it's
not in the pending batch.

This matches the original system's "broadcast once across the system"
semantic. Newer subscribers only receive events that were never dispatched
(emitted while no one was connected). For older history, the agent uses
its own backfill GET endpoint.

When/if we want per-subscriber tracking (multi-agent-per-store), we'll
extend `outbox_dispatches.handler_name` into a composite
`(handler_name, target_id)` key. Not today.

### #9 — `Subscribers()` and `PublishOrder` don't share a mutex

By design. Holding `b.mu` across the SSE handler's full `Handle` call
would serialize the broker against DB I/O — every subscriber-count check
would lock the publish path. Bad.

Instead we let the count race happen and detect it via `delivered`. Costs
us nothing (`delivered == 0` is the same `ErrTransient` outcome as
`Subscribers() == 0`); buys us a non-blocking broker.

### #10 — Two consumer goroutines persist orders concurrently

Each persist is its own transaction. The unique key on
`(source, source_event_id)` prevents duplicates at the DB level — second
write fails with `pgUniqueViolation` and the consumer maps it to
`order.ErrDuplicate` (ack-and-skip).

Both calls fire `Wake()`. Coalesces into one wake-up. The dispatcher's
batch query reads multiple events in one shot, in `occurred_at` order.

---

## What we deliberately do NOT do

- **No mutex held across handler I/O.** Would let one slow handler stall
  the broker. The cost — at-least-once duplicate delivery — is the right
  trade.
- **No per-subscriber dispatch tracking.** `outbox_dispatches.handler_name`
  is per-handler, not per-subscriber. Adequate while there's at most one
  agent per "store". Multi-agent-per-store is a future reform.
- **No `LISTEN`/`NOTIFY`.** `Wake()` is in-process only. If we ever run
  multiple `main/` replicas, we'll add a Postgres `NOTIFY` channel that
  fires `Wake()` on every replica. Not needed today.
- **No cross-handler ordering.** Handler A finishing event N before handler
  B finishes event N is not guaranteed. Within a single handler, events
  are processed `occurred_at ASC`.
- **No give-up on permanent failures.** Retries back off exponentially from
  `outbox_dispatches.attempts` (capped at 10 min) but never stop. TODO: park /
  DLQ a row after some attempt cap.

---

## Where to look

| What | File | Anchor |
|---|---|---|
| Broker fan-out + mutex | `backend/internal/infra/sse/broker.go` | `Subscribe`, `PublishOrder`, `mu` |
| SSE handler / transient mapping | `backend/internal/dispatch/handlers/sse.go` | `Handle` |
| Dispatcher loop + Wake | `backend/internal/dispatch/dispatcher.go` | `Run`, `Wake`, `tick`, `dispatchOne` |
| Domain event accumulation | `backend/internal/domain/order/events.go` | `DomainEvent`, `recordEvent`, `PullEvents` |
| Service waking on persist | `backend/internal/app/order/service.go` | `Create` (last lines) |
| HTTP endpoint | `backend/internal/transport/handler/sse.go` | `StreamOrders` |
| Wire shape | `backend/internal/transport/sse/envelope.go` | `OrderEvent`, `BuildEvent` |
| Atomic outbox emit | `backend/internal/infra/database/repositories/order_repo.go` | `Create`, `insertOutboxEvent` |
| Outbox dispatch state | `backend/internal/infra/database/repositories/outbox_repo.go` | `PendingEventsForHandler`, `MarkDispatched`, `MarkFailed` |
| Schema | `backend/sql/schema.sql`, `backend/internal/migrate/sql/00001_initial_schema.sql` | `outbox_events`, `outbox_dispatches`, `idx_outbox_dispatches_pending` |
| Dispatcher tests | `backend/internal/dispatch/dispatcher_test.go` | `TestDispatcher_*` |
| SSE handler tests | `backend/internal/dispatch/handlers/sse_test.go` | `TestSSE_*` |

---

## Quick reference: adding a new handler

1. Implement `dispatch.Handler` in
   `backend/internal/dispatch/handlers/<name>.go`.
   - `Name()` is on-disk identity. Pick once, never change without a DB
     migration of `outbox_dispatches.handler_name`.
   - `Handle()` MUST be idempotent. The dispatcher will retry on transient
     errors, on permanent errors, and across crashes.
   - Return `dispatch.ErrTransient` (wrapped) for "try me again later".
2. Construct it in `cmd/api/main.go` and append to the `allHandlers` slice
   *before* the order repo is built. The repo writes one dispatch row per
   handler in the same transaction as the order; new handlers introduced
   after startup only see events emitted from that point onward (this is
   intentional — no historical replay).
3. Optional: register OTEL metrics by adding a case in the `Hooks` callback
   wiring.
