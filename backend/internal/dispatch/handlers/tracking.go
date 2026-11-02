package handlers

import (
	"context"
	"fmt"

	"quiccpos/main/internal/dispatch"
	"quiccpos/main/internal/domain/order"
)

const HandlerNameTracking = "channel-tracking"

// TrackingRecorder is the tracking service.
type TrackingRecorder interface {
	Record(ctx context.Context, ev order.DomainEvent) error
}

// Tracking feeds the channels' live streams (main 3.0): every order.received
// / order.status_changed of a keyed channel's order becomes a stream event.
// Other orders (Brygid's, older ones) are skipped. A database failure is
// transient (the dispatcher retries).
type Tracking struct {
	rec TrackingRecorder
}

func NewTracking(rec TrackingRecorder) *Tracking { return &Tracking{rec: rec} }

func (h *Tracking) Name() string { return HandlerNameTracking }

func (h *Tracking) Handle(ctx context.Context, ev order.DomainEvent) error {
	if ev.Type != order.EventOrderReceived && ev.Type != order.EventOrderStatusChanged {
		return nil
	}
	if err := h.rec.Record(ctx, ev); err != nil {
		return fmt.Errorf("record channel event: %v: %w", err, dispatch.ErrTransient)
	}
	return nil
}
