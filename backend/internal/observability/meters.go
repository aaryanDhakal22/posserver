package observability

import (
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// MeterName is the scope used when creating instruments across main/.
const MeterName = "quiccpos/main"

// Meters bundles main's custom metric instruments. Auto instruments come
// from otelecho-style middleware, otelpgx, and otelaws.
type Meters struct {
	OrdersConsumed    metric.Int64Counter
	OrdersPersisted   metric.Int64Counter
	SQSMessagesInFlt  metric.Int64UpDownCounter
	SSEClientsActive  metric.Int64UpDownCounter
}

func NewMeters() (Meters, error) {
	m := otel.Meter(MeterName)
	var ms Meters
	var err error

	if ms.OrdersConsumed, err = m.Int64Counter(
		"orders.consumed",
		metric.WithDescription("SQS messages successfully processed as orders by main/"),
	); err != nil {
		return Meters{}, fmt.Errorf("orders.consumed: %w", err)
	}

	if ms.OrdersPersisted, err = m.Int64Counter(
		"orders.persisted",
		metric.WithDescription("Orders persisted to PostgreSQL by main/"),
	); err != nil {
		return Meters{}, fmt.Errorf("orders.persisted: %w", err)
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

	return ms, nil
}
