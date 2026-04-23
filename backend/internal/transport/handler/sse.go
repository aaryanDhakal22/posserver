package handler

import (
	"fmt"
	"net/http"
	"time"

	"quiccpos/main/internal/observability"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "quiccpos/main/sse-handler"

type SSEBroker interface {
	Subscribe() (chan []byte, func())
}

type SSEHandler struct {
	broker SSEBroker
	logger zerolog.Logger
	tracer trace.Tracer
	meters observability.Meters
}

func NewSSEHandler(broker SSEBroker, logger zerolog.Logger, meters observability.Meters) *SSEHandler {
	return &SSEHandler{
		broker: broker,
		logger: logger.With().Str("module", "sse-handler").Logger(),
		tracer: otel.Tracer(tracerName),
		meters: meters,
	}
}

// GET /api/v1/events/orders
// Streams new orders to the connected agent as Server-Sent Events.
func (h *SSEHandler) StreamOrders(c *echo.Context) error {
	ctx, span := h.tracer.Start(c.Request().Context(), "sse.connection",
		trace.WithSpanKind(trace.SpanKindServer),
	)
	defer span.End()
	c.SetRequest(c.Request().WithContext(ctx))

	w := c.Response()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		h.logger.Error().Ctx(ctx).Msg("response writer does not support flushing")
		return echo.ErrInternalServerError
	}

	ch, unsub := h.broker.Subscribe()
	defer unsub()

	h.meters.SSEClientsActive.Add(ctx, 1)
	defer h.meters.SSEClientsActive.Add(ctx, -1)

	h.logger.Info().Ctx(ctx).Msg("agent connected to SSE stream")

	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case data, ok := <-ch:
			if !ok {
				return nil
			}
			fmt.Fprintf(w, "event: order\ndata: %s\n\n", data)
			flusher.Flush()
			h.logger.Debug().Ctx(ctx).Msg("SSE order event sent")

		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()

		case <-c.Request().Context().Done():
			h.logger.Info().Ctx(ctx).Msg("agent disconnected from SSE stream")
			return nil
		}
	}
}
