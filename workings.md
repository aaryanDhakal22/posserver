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
                            │   handlers.SSE        │   decode snapshot,
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

The broker is **stateless live fan-out**. No DB, no durability. Its only job
is "deliver this frame to whichever subscribers of *this scope* are connected
*right now*." Every subscriber belongs to one `Scope = (tenant_id,
location_id)`; two scopes never share a subscriber set, so cross-tenant
fan-out is impossible by construction.

### Shape

```go
// One SSE message: event name + pre-marshaled data. Events: "order",
// "command" (duplex RPC envelope), "store_config".
type Frame struct {
    Event string
    Data  []byte
}

type Scope struct {
    TenantID   uuid.UUID
    LocationID uuid.UUID
}

type Broker struct {
    mu          sync.Mutex
    clients     map[Scope]map[chan Frame]struct{}
    devices     map[chan Frame]uuid.UUID // stream → device that opened it
    waker       Waker
    logger      zerolog.Logger
    meters      observability.Meters
    onSubscribe []func(scope Scope)      // e.g. geo.WakeLocation
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
// Subscribe = SubscribeAs(ctx, scope, uuid.Nil). The SSE handler uses
// SubscribeAs with the authenticated device id.
func (b *Broker) SubscribeAs(ctx context.Context, scope Scope, deviceID uuid.UUID) (chan Frame, func()) {
    ch := make(chan Frame, subscriberBuffer) // 256

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
        // The dispatcher implements the optional ScopeWaker: it clears this
        // scope's retry backoff (ResetDispatchBackoffForScope) and then Wakes.
        if sw, ok := b.waker.(ScopeWaker); ok {
            sw.WakeScope(ctx, scope.TenantID, scope.LocationID)
        } else {
            b.waker.Wake()
        }
    }
    for _, fn := range b.onSubscribe {
        go fn(scope) // e.g. the geo worker re-pushes what this agent hasn't acked
    }

    return ch, func() {
        b.mu.Lock()
        b.removeLocked(scope, ch) // delete + close once; double-unsub safe
        b.mu.Unlock()
    }
}
```

Notes:
- The per-subscriber channel is buffered to **256** — generous so a reconnect
  backlog or a burst never overflows it. An overflow still isn't a loss: the
  dropped order frame stays unacked and is re-sent by the redelivery loop
  (see "Delivery to the agent" below), just later.
- `Wake()` (via `WakeScope`) is fired **after** the subscriber is in `clients`,
  so the dispatcher's drain on the next tick will find the channel ready to
  receive. `WakeScope` first resets `next_attempt_at` for the scope's pending
  rows — without it, a backlog that backed off while the agent was away could
  wait up to the 30s transient cap.
- `removeLocked` re-checks that the channel is still in the scope's set
  before `delete` + `close`, so double-unsub is a no-op (no `close of closed
  channel` panic); an emptied scope is removed from `clients`.
- `DisconnectDevice(id)` closes every stream a device opened. Tenancy's
  `OnDeviceGone` hook calls it when a device is revoked or deleted (an
  admin revoke, an agent replaced by `agent/link`), so a revoked agent stops
  receiving at once; its reconnect then gets 401 and it wipes itself.
- `OnSubscribe` hooks run in their own goroutines, off the subscribe path.

### Publish

```go
func (b *Broker) Publish(ctx context.Context, scope Scope, frame Frame) int {
    // … OTEL span "sse.publish" elided …
    delivered := 0
    b.mu.Lock()
    set := b.clients[scope]
    for ch := range set {
        select {
        case ch <- frame:
            delivered++
        default:
            b.logger.Warn().Ctx(ctx).Msg("SSE subscriber channel full; dropping payload for them")
        }
    }
    b.mu.Unlock()
    // sse.events.broadcast{event_type, result: delivered|no_subscribers}
    return delivered
}

// PublishCommand wraps a duplex RPC envelope as Frame{Event: "command"}.
func (b *Broker) PublishCommand(ctx context.Context, scope Scope, data []byte) int
```

Notes:
- Takes a pre-marshaled frame — the broker doesn't know about Snapshot,
  OrderEvent, or anything order-shaped. Pure fan-out primitive. The same
  stream carries `order` frames (dispatcher), `command` frames
  (`rpc.Requester`) and mid-stream `store_config` frames
  (`StoreConfigPublisher`).
- Returns `delivered` — the count is what the SSE handler uses to detect
  "raced to zero subscribers" (race #3 below).
- Send is **non-blocking** (`select { case ch <- frame: default: }`). A
  stalled agent can never block the publish path. The cost is that one frame
  may not reach that agent; an order frame is re-sent by redelivery until
  the agent acks it.

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
    // Only OrderReceived drives an SSE push. Skipping other types (e.g. the
    // order.status_changed events floor updates record) counts as a
    // successful dispatch — the row is marked dispatched, nothing echoes
    // back to the agent.
    if ev.Type != order.EventOrderReceived {
        return nil
    }

    // Fat-payload contract: the snapshot IS the payload. Decode it (it also
    // carries the scope) — no DB read; no chance the aggregate has mutated
    // since this event was emitted.
    var snap order.Snapshot
    if err := json.Unmarshal(ev.Payload, &snap); err != nil {
        return fmt.Errorf("decode snapshot from outbox payload: %w", err)
    }
    scope := sseBroker.Scope{TenantID: …snap.TenantID, LocationID: …snap.LocationID}

    // Fast-path: nobody of this scope connected. ErrTransient leaves the row
    // pending; the agent's (re)subscribe WakeScope (or a later tick) retries.
    if h.publisher.SubscribersFor(scope) == 0 {
        return fmt.Errorf("no SSE subscribers for scope: %w", dispatch.ErrTransient)
    }

    wireEv := sseTransport.EventFromSnapshot(ctx, snap, true)
    data, err := json.Marshal(wireEv)
    if err != nil {
        return fmt.Errorf("marshal SSE event: %w", err)
    }

    delivered := h.publisher.Publish(ctx, scope, sseBroker.Frame{Event: sseBroker.EventOrder, Data: data})
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
    ev := pe.Event // PendingEvent{Event, TenantID, Attempts} — attempts from outbox_dispatches
    // …span setup elided…
    err := h.Handle(ctx, ev)
    if err == nil {
        d.repo.MarkDispatched(ctx, ev.ID, h.Name())
        if d.hooks.OnEventDispatched != nil {
            d.hooks.OnEventDispatched(ctx, h.Name(), ev.Type, pe.TenantID)
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
dispatcher into the OTEL meters without the dispatcher importing `metric`;
they get the event's tenant id (`outbox_events.tenant_id`) so the outbox
counters carry the `tenant` / `location` store labels.

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
    tenant_id       uuid        NOT NULL,   -- scope, so a reconnect can reset
    location_id     uuid        NOT NULL,   -- just that scope's backoff
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
SELECT e.id, e.aggregate_id, e.tenant_id, e.location_id, e.type, e.payload,
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
`ResetDispatchBackoffForScope` (called through `WakeScope` when an agent
subscribes) sets `next_attempt_at = NULL` on the scope's still-pending rows,
joined through `outbox_events.tenant_id` / `location_id`
(`idx_outbox_events_scope`).

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
       └→ OnCreated hooks (geo: insert order_geo for a delivery order, wake the geo worker)

T+3    Dispatcher's `<-d.wake` fires → tick() → runHandler(SSE)
       └→ PendingEventsForHandler("sse-broadcast", 50) → 1 row
       └→ SSE.Handle(ev)
            └→ decode Snapshot from ev.Payload → scope
            └→ SubscribersFor(scope) == 1 (the store's connected agent)
            └→ broker.Publish(scope, Frame{order, data}) → delivered = 1
       └→ MarkDispatched(eventID, "sse-broadcast")

T+4    HTTP loop in StreamOrders:
       └→ <-ch fires
       └→ fmt.Fprintf(w, "event: order\ndata: %s\n\n", data)
       └→ flusher.Flush()
       └→ agent receives the event
```

If the agent isn't connected at T+3:
- `SubscribersFor(scope) == 0` → handler returns `ErrTransient`.
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
       └→ For each: SSE.Handle → broker.Publish(scope, order frame) → MarkDispatched

T+…    HTTP loop drains the buffered channel, agent sees orders in order.
       Agent stores each order and acks it (RPC order.ack → orders.agent_ack_at).
       ~3 s after the subscribe, the geo worker re-pushes this store's unacked order.geo rows.
```

Cap: the dispatcher batch size (default 50). If the backlog exceeds that,
the dispatcher catches up across multiple ticks. Anything still unacked is
re-sent by the redelivery loop; the agent also pages `GET /api/v1/orders`
after every reconnect to fill gaps.

---

## Delivery to the agent: acks + redelivery (`internal/app/agentsync`)

"Dispatched" only means "queued on a live stream". The end-to-end signal is
the agent's **`order.ack`** RPC report (`{order_ids}` → `orders.agent_ack_at`).
`RunRedeliver` ticks every second and re-publishes, to the scope's connected
agent, every order still in status `received` and unacked that was received
more than `ORDER_REDELIVER_AFTER` (default 10 s) ago and within the last
12 h (≤ 200 per tick), then again every `ORDER_REDELIVER_AFTER` (an
in-memory last-sent map); nothing is sent while no agent of that scope is
connected. A redelivered frame is built from the order as it is now in the
DB (`GetByID` → `Snapshot`), not from the outbox payload. The agent
treats a redelivery as a no-op and acks again. This is what makes a dropped
frame (full buffer, a stream that died mid-write, a crash) harmless, and why a
pytest that plays the agent over SSE sees the same order frame again unless
it acks. Orders that existed before migration `00009` count as acked.
`orders.unacked` (gauge: unacked for over a minute) should stay 0.

---

## Race conditions

The interesting ones, with how each is handled.

### #1 — Subscribe insert vs concurrent Publish

Two goroutines: one calling `Subscribe`, another finishing a `Publish`.

`sync.Mutex` on `clients` serializes them. Either:
- Subscribe wins → its channel is in the map before Publish iterates → it
  gets the event.
- Publish wins → Subscribe registers afterward → misses this event but
  triggers `Wake()`. The dispatcher's next tick replays anything still
  `dispatched_at IS NULL`.

Either way, no event lost.

### #2 — Slow subscriber (channel full)

Per-subscriber channel is buffered to 256. If the agent reads slowly:

```go
for ch := range b.clients[scope] {
    select {
    case ch <- frame:
        delivered++
    default:
        b.logger.Warn().Ctx(ctx).Msg("SSE subscriber channel full; dropping payload for them")
    }
}
```

Non-blocking send drops the frame **for that subscriber only**. The
publish path keeps moving — one stalled agent can't gum up the others or
the dispatcher.

Cost: that subscriber misses that frame. An order frame comes back through
redelivery (it stays unacked); a `command` request times out on main's side
and the caller retries or reports; a `store_config` comes back on the next
connect's handshake.

### #3 — Subscriber count check vs actual delivery

Inside `SSE.Handle`:

```go
if h.publisher.SubscribersFor(scope) == 0 {
    return ErrTransient   // skip the marshal + publish
}
// … wrap snapshot as OrderEvent, marshal …
delivered := h.publisher.Publish(ctx, scope, frame)
if delivered == 0 {
    return ErrTransient   // raced — everyone disconnected
}
```

The `SubscribersFor()` check is an optimization (skip the work when
obviously no one of that scope is listening). Correctness rests on
`delivered == 0 → ErrTransient`. A subscriber count of 1 at the check moment
can still drop to 0 by the time Publish runs; the second check catches that
and the row stays pending.

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
func (b *Broker) removeLocked(scope Scope, ch chan Frame) {
    if set, ok := b.clients[scope]; ok {
        if _, ok := set[ch]; ok {
            delete(set, ch)
            delete(b.devices, ch)
            close(ch)
        }
        if len(set) == 0 {
            delete(b.clients, scope)
        }
    }
}
```

The `if _, ok := set[ch]; ok` check makes the second call a no-op,
preventing the `close of closed channel` panic. The same guard covers a
stream already closed by `DisconnectDevice` (revoked device) whose handler
then unsubscribes.

### #6 — Handler runs, MarkDispatched fails (DB hiccup, crash)

The handler delivered to the agent, but the dispatcher couldn't update
`outbox_dispatches`. The row is still `dispatched_at = NULL`. Next tick,
the dispatcher tries again. Handler runs again. Agent receives a duplicate.

This is **at-least-once** semantics. The contract is that handlers must be
idempotent. The agent satisfies this by deduplicating on aggregate `id`
(and redelivery relies on the same property).

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
semantic for the outbox. Newer subscribers only receive events that were
never dispatched (emitted while no one was connected) — plus, separately,
every order the agent never acked (redelivery), and whatever its own
catch-up `GET /api/v1/orders?status=received&from=…` finds.

When/if we want per-subscriber tracking (multi-agent-per-store), we'll
extend `outbox_dispatches.handler_name` into a composite
`(handler_name, target_id)` key. Not today.

### #9 — `SubscribersFor()` and `Publish` don't share a mutex

By design. Holding `b.mu` across the SSE handler's full `Handle` call
would serialize the broker against DB I/O — every subscriber-count check
would lock the publish path. Bad.

Instead we let the count race happen and detect it via `delivered`. Costs
us nothing (`delivered == 0` is the same `ErrTransient` outcome as
`SubscribersFor() == 0`); buys us a non-blocking broker.

### #10 — Two consumer goroutines persist orders concurrently

Each persist is its own transaction. The unique key on
`(tenant_id, source, source_event_id)` prevents duplicates at the DB level — second
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
| Broker fan-out + mutex | `backend/internal/infra/sse/broker.go` | `SubscribeAs`, `Publish`, `DisconnectDevice`, `mu` |
| Ack + redelivery | `backend/internal/app/agentsync/agentsync.go` | `HandleAck`, `RunRedeliver`, `resend` |
| SSE handler / transient mapping | `backend/internal/dispatch/handlers/sse.go` | `Handle` |
| Dispatcher loop + Wake | `backend/internal/dispatch/dispatcher.go` | `Run`, `Wake`, `tick`, `dispatchOne` |
| Domain event accumulation | `backend/internal/domain/order/events.go` | `DomainEvent`, `recordEvent`, `PullEvents` |
| Service waking on persist | `backend/internal/app/order/service.go` | `Create` (last lines) |
| HTTP endpoint | `backend/internal/transport/handler/sse.go` | `StreamOrders` |
| Wire shape | `backend/internal/transport/sse/envelope.go` | `OrderEvent`, `EventFromSnapshot` |
| Atomic outbox emit | `backend/internal/infra/database/repositories/order_repo.go` | `Create`, `insertOutboxEvent` |
| Outbox dispatch state | `backend/internal/infra/database/repositories/outbox_repo.go` | `PendingEventsForHandler`, `MarkDispatched`, `MarkFailed`, `ResetBackoffForScope` |
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
