package sse

import (
	"context"
	"encoding/json"
	"sync"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/transport/dto"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "quiccpos/main/sse"

// Broker fans out published byte slices to all subscribed SSE clients.
type Broker struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

func New() *Broker {
	return &Broker{clients: make(map[chan []byte]struct{})}
}

// Subscribe returns a buffered channel that receives published payloads and a
// function to call when the client disconnects.
func (b *Broker) Subscribe() (chan []byte, func()) {
	ch := make(chan []byte, 8)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.clients, ch)
		close(ch)
		b.mu.Unlock()
	}
}

// Subscribers returns the current fan-out width (used for metrics).
func (b *Broker) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.clients)
}

// sseEnvelope extends the existing order DTO with trace-context fields. The
// agent-side SSE client reads `_traceparent` to rebuild the parent context so
// its spans chain into main's sse.publish span.
type sseEnvelope struct {
	dto.Order
	Traceparent string `json:"_traceparent,omitempty"`
	Tracestate  string `json:"_tracestate,omitempty"`
}

// PublishOrder satisfies orderSvc.OrderPublisher. It creates an sse.publish
// span, injects the current trace context into the payload, marshals it, and
// fans it out to every subscribed client.
func (b *Broker) PublishOrder(ctx context.Context, o order.Order) {
	tracer := otel.Tracer(tracerName)
	ctx, span := tracer.Start(ctx, "sse.publish",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.Int("order.id", o.OrderID),
			attribute.String("order.service_type", o.ServiceType),
			attribute.Int("sse.subscribers", b.Subscribers()),
		),
	)
	defer span.End()

	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	payload := sseEnvelope{
		Order:       dto.FromDomain(o),
		Traceparent: carrier["traceparent"],
		Tracestate:  carrier["tracestate"],
	}
	data, err := json.Marshal(payload)
	if err != nil {
		span.RecordError(err)
		return
	}
	b.publish(data)
}

// publish sends raw bytes to every subscribed client. Slow clients are skipped
// (non-blocking send) so a stalled agent never blocks the SQS consumer.
func (b *Broker) publish(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.clients {
		select {
		case ch <- data:
		default:
		}
	}
}
