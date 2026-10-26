package observability

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// MeterName is the scope used when creating instruments across main/.
const MeterName = "quiccpos/main"

// PendingDispatchSource is satisfied by the outbox repo. The observable
// gauge callback queries it once per scrape, per registered handler, to
// publish the current pending-dispatch depth per store (every tenant, 0 when
// drained).
type PendingDispatchSource interface {
	CountPendingByTenant(ctx context.Context, handlerName string) (map[uuid.UUID]int, error)
}

// Meters bundles main's custom metric instruments. Auto instruments come
// from echo middleware, otelpgx, and otelaws.
type Meters struct {
	OrdersConsumed   metric.Int64Counter
	OrdersPersisted  metric.Int64Counter
	OrdersCastFailed metric.Int64Counter
	SQSMessagesInFlt metric.Int64UpDownCounter
	SSEClientsActive metric.Int64UpDownCounter

	// Outbox / dispatcher metrics. Per-handler attribution is on the
	// `handler` attribute; per-event-type is on `event_type`.
	OutboxDispatched metric.Int64Counter
	OutboxFailed     metric.Int64Counter
	OutboxPending    metric.Int64ObservableGauge

	// OrdersPersistFailed counts consumed orders whose persistence errored
	// (left on the queue for retry). Attribute: source.
	OrdersPersistFailed metric.Int64Counter

	// SSEEventsBroadcast counts fan-out attempts through the broker.
	// Attributes: event_type (order|command), result (delivered|no_subscribers).
	SSEEventsBroadcast metric.Int64Counter

	// Duplex RPC channel (main↔agent). All carry a `type` attribute (the
	// message type). RPCReplies/RPCInbound also carry `result`.
	RPCRequests  metric.Int64Counter
	RPCOffline   metric.Int64Counter
	RPCTimeouts  metric.Int64Counter
	RPCReplies   metric.Int64Counter
	RPCInbound   metric.Int64Counter
	RPCRoundtrip metric.Float64Histogram

	// Menu module. MenuReads: surface (manager|operator). MenuMutations:
	// entity (menu|category|item|modifier_group) + op (create|update|delete).
	MenuReads     metric.Int64Counter
	MenuMutations metric.Int64Counter
	// OrderItemsRouted counts top-level order items by kitchen routing result
	// (attribute routing = matched|unmatched|unassigned).
	OrderItemsRouted metric.Int64Counter
	// FloorEventsReceived counts agent floor events by type + result
	// (applied|duplicate|stale|not_found|invalid).
	FloorEventsReceived metric.Int64Counter

	// Store + operator auth. Denied variants carry a `reason` attribute.
	AuthStoreAllowed    metric.Int64Counter
	AuthStoreDenied     metric.Int64Counter
	AuthOperatorAllowed metric.Int64Counter
	AuthOperatorDenied  metric.Int64Counter

	// Operator revocable-token lifecycle.
	OperatorTokensMinted  metric.Int64Counter
	OperatorTokensRevoked metric.Int64Counter

	// PrintersLiveQueries counts /me/printers/live lookups. Attribute:
	// result (live|cache|offline).
	PrintersLiveQueries metric.Int64Counter
}

func NewMeters() (Meters, error) {
	m := otel.Meter(MeterName)
	var ms Meters
	var err error

	if ms.OrdersConsumed, err = m.Int64Counter(
		"orders.consumed",
		metric.WithDescription("SQS messages successfully cast as orders by main/"),
	); err != nil {
		return Meters{}, fmt.Errorf("orders.consumed: %w", err)
	}

	if ms.OrdersPersisted, err = m.Int64Counter(
		"orders.persisted",
		metric.WithDescription("Orders persisted to PostgreSQL by main/"),
	); err != nil {
		return Meters{}, fmt.Errorf("orders.persisted: %w", err)
	}

	if ms.OrdersCastFailed, err = m.Int64Counter(
		"orders.cast.failed",
		metric.WithDescription("Orders rejected by the source caster (malformed payload, validation failure)"),
	); err != nil {
		return Meters{}, fmt.Errorf("orders.cast.failed: %w", err)
	}

	if ms.SQSMessagesInFlt, err = m.Int64UpDownCounter(
		"sqs.messages.in_flight",
		metric.WithDescription("SQS messages currently being processed (received but not deleted)"),
	); err != nil {
		return Meters{}, fmt.Errorf("sqs.messages.in_flight: %w", err)
	}

	if ms.SSEClientsActive, err = m.Int64UpDownCounter(
		"sse.clients.active",
		metric.WithDescription("Agents currently connected to the SSE stream"),
	); err != nil {
		return Meters{}, fmt.Errorf("sse.clients.active: %w", err)
	}

	if ms.OutboxDispatched, err = m.Int64Counter(
		"outbox.events.dispatched",
		metric.WithDescription("Domain events successfully dispatched (per handler, per event type)"),
	); err != nil {
		return Meters{}, fmt.Errorf("outbox.events.dispatched: %w", err)
	}

	if ms.OutboxFailed, err = m.Int64Counter(
		"outbox.events.failed",
		metric.WithDescription("Domain event dispatch attempts that returned an error (transient + permanent)"),
	); err != nil {
		return Meters{}, fmt.Errorf("outbox.events.failed: %w", err)
	}

	if ms.OutboxPending, err = m.Int64ObservableGauge(
		"outbox.events.pending",
		metric.WithDescription("Pending dispatch rows per handler (dispatched_at IS NULL)"),
	); err != nil {
		return Meters{}, fmt.Errorf("outbox.events.pending: %w", err)
	}

	if ms.OrdersPersistFailed, err = m.Int64Counter(
		"orders.persist.failed",
		metric.WithDescription("Consumed orders whose persistence errored (left on the queue for retry)"),
	); err != nil {
		return Meters{}, fmt.Errorf("orders.persist.failed: %w", err)
	}

	if ms.SSEEventsBroadcast, err = m.Int64Counter(
		"sse.events.broadcast",
		metric.WithDescription("SSE fan-out attempts (per event_type, per result)"),
	); err != nil {
		return Meters{}, fmt.Errorf("sse.events.broadcast: %w", err)
	}

	if ms.RPCRequests, err = m.Int64Counter(
		"rpc.requests",
		metric.WithDescription("Duplex RPC main→agent command attempts"),
	); err != nil {
		return Meters{}, fmt.Errorf("rpc.requests: %w", err)
	}

	if ms.RPCOffline, err = m.Int64Counter(
		"rpc.offline",
		metric.WithDescription("Duplex RPC requests that failed because no agent was connected"),
	); err != nil {
		return Meters{}, fmt.Errorf("rpc.offline: %w", err)
	}

	if ms.RPCTimeouts, err = m.Int64Counter(
		"rpc.timeouts",
		metric.WithDescription("Duplex RPC requests that timed out awaiting a reply"),
	); err != nil {
		return Meters{}, fmt.Errorf("rpc.timeouts: %w", err)
	}

	if ms.RPCReplies, err = m.Int64Counter(
		"rpc.replies",
		metric.WithDescription("Duplex RPC replies received for a pending request (result=ok|error)"),
	); err != nil {
		return Meters{}, fmt.Errorf("rpc.replies: %w", err)
	}

	if ms.RPCInbound, err = m.Int64Counter(
		"rpc.inbound",
		metric.WithDescription("Agent-initiated duplex messages handled (result=ok|error|unknown)"),
	); err != nil {
		return Meters{}, fmt.Errorf("rpc.inbound: %w", err)
	}

	if ms.RPCRoundtrip, err = m.Float64Histogram(
		"rpc.roundtrip.duration",
		metric.WithDescription("Duplex RPC request send → reply/timeout latency"),
		metric.WithUnit("ms"),
	); err != nil {
		return Meters{}, fmt.Errorf("rpc.roundtrip.duration: %w", err)
	}

	if ms.MenuReads, err = m.Int64Counter(
		"menu.reads",
		metric.WithDescription("Menu reads (per surface: manager|operator)"),
	); err != nil {
		return Meters{}, fmt.Errorf("menu.reads: %w", err)
	}

	if ms.MenuMutations, err = m.Int64Counter(
		"menu.mutations",
		metric.WithDescription("Menu mutations (per entity, per op)"),
	); err != nil {
		return Meters{}, fmt.Errorf("menu.mutations: %w", err)
	}

	if ms.OrderItemsRouted, err = m.Int64Counter(
		"orders.items.routed",
		metric.WithDescription("Order line items routed to kitchen printers at ingest (attribute routing: matched|unmatched|unassigned)"),
	); err != nil {
		return Meters{}, fmt.Errorf("orders.items.routed: %w", err)
	}

	if ms.FloorEventsReceived, err = m.Int64Counter(
		"floor.events.received",
		metric.WithDescription("Agent floor events applied by main (attributes type, result)"),
	); err != nil {
		return Meters{}, fmt.Errorf("floor.events.received: %w", err)
	}

	if ms.AuthStoreAllowed, err = m.Int64Counter(
		"auth.store.allowed",
		metric.WithDescription("Store (X-STORE-API-KEY) auth successes"),
	); err != nil {
		return Meters{}, fmt.Errorf("auth.store.allowed: %w", err)
	}

	if ms.AuthStoreDenied, err = m.Int64Counter(
		"auth.store.denied",
		metric.WithDescription("Store auth denials (per reason)"),
	); err != nil {
		return Meters{}, fmt.Errorf("auth.store.denied: %w", err)
	}

	if ms.AuthOperatorAllowed, err = m.Int64Counter(
		"auth.operator.allowed",
		metric.WithDescription("Operator (qopr_ bearer) auth successes"),
	); err != nil {
		return Meters{}, fmt.Errorf("auth.operator.allowed: %w", err)
	}

	if ms.AuthOperatorDenied, err = m.Int64Counter(
		"auth.operator.denied",
		metric.WithDescription("Operator auth denials (per reason)"),
	); err != nil {
		return Meters{}, fmt.Errorf("auth.operator.denied: %w", err)
	}

	if ms.OperatorTokensMinted, err = m.Int64Counter(
		"operator.tokens.minted",
		metric.WithDescription("Operator read-only bearer tokens minted"),
	); err != nil {
		return Meters{}, fmt.Errorf("operator.tokens.minted: %w", err)
	}

	if ms.OperatorTokensRevoked, err = m.Int64Counter(
		"operator.tokens.revoked",
		metric.WithDescription("Operator token-revocation actions"),
	); err != nil {
		return Meters{}, fmt.Errorf("operator.tokens.revoked: %w", err)
	}

	if ms.PrintersLiveQueries, err = m.Int64Counter(
		"printers.live.queries",
		metric.WithDescription("Live printer-roster lookups (result=live|cache|offline)"),
	); err != nil {
		return Meters{}, fmt.Errorf("printers.live.queries: %w", err)
	}

	return ms, nil
}

// RegisterPendingDispatch wires the outbox.events.pending observable gauge
// to the outbox repo for each handler name. Call once during bootstrap
// after the dispatcher is built (so handler names are known).
func RegisterPendingDispatch(meters Meters, src PendingDispatchSource, handlerNames []string) error {
	if len(handlerNames) == 0 {
		return nil
	}
	m := otel.Meter(MeterName)
	_, err := m.RegisterCallback(
		func(ctx context.Context, o metric.Observer) error {
			for _, name := range handlerNames {
				byTenant, err := src.CountPendingByTenant(ctx, name)
				if err != nil {
					// Don't fail the scrape on a transient DB error; just
					// skip this handler's reading and try again next tick.
					continue
				}
				for tenantID, n := range byTenant {
					o.ObserveInt64(meters.OutboxPending, int64(n),
						WithStore(ctx, tenantID, attribute.String("handler", name)),
					)
				}
			}
			return nil
		},
		meters.OutboxPending,
	)
	return err
}
