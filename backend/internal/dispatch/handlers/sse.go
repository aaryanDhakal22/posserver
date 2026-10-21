// Package handlers contains the concrete dispatcher Handlers main/ ships
// today. Each file in this package is a self-contained handler that knows
// how to translate one or more domain event types into a side effect.
//
// Conventions:
//   - Name() must be stable across deploys — it's the primary key in
//     outbox_dispatches. Renaming it strands historical events undelivered.
//   - Handle() must be idempotent.
//   - Handle() should return dispatch.ErrTransient (wrapped) for failures
//     where a retry might succeed.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"

	"quiccpos/main/internal/dispatch"
	"quiccpos/main/internal/domain/order"
	sseBroker "quiccpos/main/internal/infra/sse"
	sseTransport "quiccpos/main/internal/transport/sse"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const (
	HandlerNameSSE = "sse-broadcast"
)

// SSEPublisher is satisfied by *sse.Broker. Kept narrow so the handler can
// be unit-tested with a fake.
type SSEPublisher interface {
	Publish(ctx context.Context, scope sseBroker.Scope, frame sseBroker.Frame) int
	SubscribersFor(scope sseBroker.Scope) int
}

// SSE adapts the live SSE broker to the dispatch.Handler interface.
//
// Subscribes to: order.received. Snapshot carries (tenant_id, location_id),
// so the handler routes to the right scope's subscribers — agents only ever
// see their own location's orders.
type SSE struct {
	publisher SSEPublisher
	logger    zerolog.Logger
}

func NewSSE(publisher SSEPublisher, lg zerolog.Logger) *SSE {
	return &SSE{
		publisher: publisher,
		logger:    lg.With().Str("module", "handler-sse").Logger(),
	}
}

func (h *SSE) Name() string { return HandlerNameSSE }

func (h *SSE) Handle(ctx context.Context, ev order.DomainEvent) error {
	if ev.Type != order.EventOrderReceived {
		return nil
	}

	var snap order.Snapshot
	if err := json.Unmarshal(ev.Payload, &snap); err != nil {
		return fmt.Errorf("decode snapshot from outbox payload: %w", err)
	}

	tenantUUID, err := uuid.Parse(snap.TenantID)
	if err != nil {
		return fmt.Errorf("invalid tenant_id in snapshot: %w", err)
	}
	locationUUID, err := uuid.Parse(snap.LocationID)
	if err != nil {
		return fmt.Errorf("invalid location_id in snapshot: %w", err)
	}
	scope := sseBroker.Scope{TenantID: tenantUUID, LocationID: locationUUID}

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
		return fmt.Errorf("publish raced to zero subscribers: %w", dispatch.ErrTransient)
	}
	return nil
}

var _ dispatch.Handler = (*SSE)(nil)
