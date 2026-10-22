package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	tenancyApp "quiccpos/main/internal/app/tenancy"
	"quiccpos/main/internal/domain/tenancy"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/observability"
	appMiddleware "quiccpos/main/internal/transport/middleware"
	sseTransport "quiccpos/main/internal/transport/sse"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "quiccpos/main/sse-handler"

// SSEBroker is the slice of *sse.Broker the handler uses. Stays narrow so
// tests can swap a fake without standing up the real broker.
type SSEBroker interface {
	Subscribe(ctx context.Context, scope sseBroker.Scope) (chan sseBroker.Frame, func())
	SubscribeAs(ctx context.Context, scope sseBroker.Scope, deviceID uuid.UUID) (chan sseBroker.Frame, func())
}

type SSEHandler struct {
	broker  SSEBroker
	tenancy *tenancyApp.Service
	logger  zerolog.Logger
	tracer  trace.Tracer
	meters  observability.Meters
}

func NewSSEHandler(broker SSEBroker, tenancy *tenancyApp.Service, logger zerolog.Logger, meters observability.Meters) *SSEHandler {
	return &SSEHandler{
		broker:  broker,
		tenancy: tenancy,
		logger:  logger.With().Str("module", "sse-handler").Logger(),
		tracer:  otel.Tracer(tracerName),
		meters:  meters,
	}
}

// StreamOrders streams orders for the authenticated device's (tenant_id,
// location_id) scope. On every connect, it sends a single `store_config`
// frame containing the full operating context (tenant + location + printers)
// before any order frames — so the agent can rebuild its local store_config
// table without an out-of-band fetch.
func (h *SSEHandler) StreamOrders(c *echo.Context) error {
	dev := appMiddleware.DeviceFromContext(c.Request().Context())
	if dev == nil {
		// Middleware should have rejected this already; defensive check.
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
	scope := sseBroker.Scope{TenantID: dev.TenantID, LocationID: dev.LocationID}

	ctx, span := h.tracer.Start(c.Request().Context(), "sse.connection",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("tenant.id", dev.TenantID.String()),
			attribute.String("location.id", dev.LocationID.String()),
		),
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

	// 1. Handshake — emit store_config BEFORE subscribing to the broker so
	//    the agent has its operating context before order events start
	//    landing.
	if err := h.writeStoreConfig(ctx, w, flusher); err != nil {
		h.logger.Error().Ctx(ctx).Err(err).Msg("store_config handshake failed; closing connection")
		return err
	}

	// 2. Subscribe + main loop.
	deviceID := uuid.Nil
	if dev := appMiddleware.DeviceFromContext(ctx); dev != nil {
		deviceID = dev.ID
	}
	ch, unsub := h.broker.SubscribeAs(ctx, scope, deviceID)
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
		case frame, ok := <-ch:
			if !ok {
				return nil
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", frame.Event, frame.Data)
			flusher.Flush()
			h.logger.Debug().Ctx(ctx).Str("sse_event", frame.Event).Msg("SSE frame sent")

		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()

		case <-c.Request().Context().Done():
			h.logger.Info().Ctx(ctx).Msg("agent disconnected from SSE stream")
			return nil
		}
	}
}

// writeStoreConfig fetches the tenant, location, and printers for the
// agent's scope (resolved by the auth middleware), marshals the canonical
// StoreContext, and writes it as the first event on the SSE stream. Errors
// here close the connection — the agent can't operate without a starter
// pack.
func (h *SSEHandler) writeStoreConfig(ctx context.Context, w http.ResponseWriter, flusher http.Flusher) error {
	dev := appMiddleware.DeviceFromContext(ctx)
	if dev == nil {
		return fmt.Errorf("no device in context")
	}
	body, printerCount, err := loadStoreConfig(ctx, h.tenancy, dev.TenantID, dev.LocationID)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", sseBroker.EventStoreConfig, body)
	flusher.Flush()
	h.logger.Debug().Ctx(ctx).
		Str("tenant_id", dev.TenantID.String()).
		Str("location_id", dev.LocationID.String()).
		Int("printer_count", printerCount).
		Msg("store_config handshake frame sent")
	return nil
}

// loadStoreConfig loads + marshals the StoreContext for one scope. Shared by
// the connect handshake and the live push (StoreConfigPublisher).
func loadStoreConfig(ctx context.Context, svc *tenancyApp.Service, tenantID, locationID uuid.UUID) ([]byte, int, error) {
	tenant, err := svc.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, 0, fmt.Errorf("load tenant: %w", err)
	}
	location, err := svc.GetLocation(ctx, locationID)
	if err != nil {
		return nil, 0, fmt.Errorf("load location: %w", err)
	}
	printers, err := svc.ListPrintersByLocation(ctx, locationID)
	if err != nil {
		return nil, 0, fmt.Errorf("load printers: %w", err)
	}
	body, err := json.Marshal(buildStoreContext(*tenant, *location, printers))
	if err != nil {
		return nil, 0, fmt.Errorf("marshal store_config: %w", err)
	}
	return body, len(printers), nil
}

// FramePublisher is the publish half of *sse.Broker.
type FramePublisher interface {
	Publish(ctx context.Context, scope sseBroker.Scope, frame sseBroker.Frame) int
}

// StoreConfigPublisher pushes a fresh `store_config` frame to a location's
// connected agent the moment its location or printers change in /app/store —
// the agent applies mid-stream frames (rebuilds its printer roster) with no
// reconnect. Best-effort: if no agent is connected, or its buffer is full, the
// next connect's handshake carries the same config anyway.
type StoreConfigPublisher struct {
	broker  FramePublisher
	tenancy *tenancyApp.Service
	logger  zerolog.Logger
}

func NewStoreConfigPublisher(broker FramePublisher, tenancy *tenancyApp.Service, logger zerolog.Logger) *StoreConfigPublisher {
	return &StoreConfigPublisher{broker: broker, tenancy: tenancy, logger: logger.With().Str("module", "store-config-push").Logger()}
}

// Publish is nil-safe so handlers can call it unconditionally.
func (p *StoreConfigPublisher) Publish(ctx context.Context, tenantID, locationID uuid.UUID) {
	if p == nil || p.broker == nil {
		return
	}
	body, _, err := loadStoreConfig(ctx, p.tenancy, tenantID, locationID)
	if err != nil {
		p.logger.Warn().Ctx(ctx).Err(err).Str("location_id", locationID.String()).Msg("store_config push: load failed")
		return
	}
	n := p.broker.Publish(ctx, sseBroker.Scope{TenantID: tenantID, LocationID: locationID},
		sseBroker.Frame{Event: sseBroker.EventStoreConfig, Data: body})
	p.logger.Debug().Ctx(ctx).Str("location_id", locationID.String()).Int("delivered", n).Msg("store_config pushed")
}

func buildStoreContext(t tenancy.Tenant, l tenancy.Location, printers []tenancy.Printer) sseTransport.StoreContext {
	tCfg := t.Config
	if tCfg == nil {
		tCfg = map[string]any{}
	}
	lCfg := l.Config
	if lCfg == nil {
		lCfg = map[string]any{}
	}
	prs := make([]sseTransport.PrinterContext, 0, len(printers))
	for _, p := range printers {
		prs = append(prs, sseTransport.PrinterContext{
			ID:        p.ID.String(),
			Name:      p.Name,
			IP:        p.IP,
			IsReceipt: p.IsReceipt,
			MAC:       p.MAC,
			UpdatedAt: p.UpdatedAt,
		})
	}
	return sseTransport.StoreContext{
		Tenant: sseTransport.TenantContext{
			ID:     t.ID.String(),
			Slug:   t.Slug,
			Name:   t.Name,
			Config: tCfg,
		},
		Location: sseTransport.LocationContext{
			ID:             l.ID.String(),
			Slug:           l.Slug,
			Name:           l.Name,
			Timezone:       l.Timezone,
			DailyResetHour: l.DailyResetHour,
			Phone:          l.Phone,
			AddressLine1:   l.AddressLine1,
			AddressLine2:   l.AddressLine2,
			Config:         lCfg,

			Lat:             l.Lat,
			Lng:             l.Lng,
			DeliveryRadiusM: l.DeliveryRadiusM,
			Map:             mapContext(l),
		},
		Printers: prs,
	}
}

// mapContext is the location's published map, nil when none.
func mapContext(l tenancy.Location) *sseTransport.MapContext {
	if l.MapURL == "" {
		return nil
	}
	bbox := l.MapBBox
	if bbox == nil {
		bbox = []float64{}
	}
	return &sseTransport.MapContext{URL: l.MapURL, SHA256: l.MapSHA256, Version: l.MapVersion, BBox: bbox}
}
