package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"quiccpos/main/internal/app/tracking"
	"quiccpos/main/internal/shared/ratelimit"
	appMiddleware "quiccpos/main/internal/transport/middleware"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// TrackingHandler serves order tracking (main 3.0 step 6): a channel's
// /channel/v1/orders/:id/status and /channel/v1/events (track permission),
// and a customer's /track/v1/:token (no key).
type TrackingHandler struct {
	svc    *tracking.Service
	public *ratelimit.Window // per IP
	logger zerolog.Logger
}

func NewTrackingHandler(svc *tracking.Service, logger zerolog.Logger) *TrackingHandler {
	return &TrackingHandler{svc: svc, public: ratelimit.New(120, time.Minute), logger: logger.With().Str("module", "tracking-handler").Logger()}
}

func trackChannel(c *echo.Context) (bool, error) {
	ch, ok := appMiddleware.ChannelFromContext(c.Request().Context())
	if !ok {
		return false, c.JSON(http.StatusUnauthorized, errCode("missing channel key", "unauthorized"))
	}
	if !ch.Permissions.Track {
		return false, c.JSON(http.StatusForbidden, errCode("this channel may not follow orders", "forbidden"))
	}
	return true, nil
}

// GET /channel/v1/orders/:id/status
func (h *TrackingHandler) Status(c *echo.Context) error {
	if ok, err := trackChannel(c); !ok {
		return err
	}
	ch, _ := appMiddleware.ChannelFromContext(c.Request().Context())
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusNotFound, errCode("order not found", "not_found"))
	}
	st, err := h.svc.ChannelStatus(c.Request().Context(), ch.ID, ch.TenantID, ch.LocationID, id)
	if errors.Is(err, tracking.ErrNotFound) {
		return c.JSON(http.StatusNotFound, errCode("order not found", "not_found"))
	}
	if err != nil {
		h.logger.Error().Err(err).Msg("order status")
		return c.JSON(http.StatusInternalServerError, errCode("status unavailable", "internal"))
	}
	return c.JSON(http.StatusOK, st)
}

// GET /channel/v1/events — the channel's orders as they change (SSE). Each
// event: `id: <n>`, `event: order.status`, data = the order's state. Resume
// with Last-Event-ID (or ?last_event_id=) within 24 h; an id no longer kept
// gets one `event: reset` (re-read your orders), then the live stream.
func (h *TrackingHandler) Events(c *echo.Context) error {
	if ok, err := trackChannel(c); !ok {
		return err
	}
	ch, _ := appMiddleware.ChannelFromContext(c.Request().Context())
	ctx := c.Request().Context()
	var last int64
	if s := c.Request().Header.Get("Last-Event-ID"); s != "" {
		last, _ = strconv.ParseInt(s, 10, 64)
	} else if s := c.QueryParam("last_event_id"); s != "" {
		last, _ = strconv.ParseInt(s, 10, 64)
	}

	wake, cancel := h.svc.Subscribe(ch.ID)
	defer cancel()
	w := c.Response()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return echo.ErrInternalServerError
	}
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()

	// send writes every event after `last`; an id that is no longer kept
	// gets one reset, then the stream continues from the newest event.
	send := func() error {
		for {
			evs, reset, err := h.svc.EventsAfter(ctx, ch.ID, last, 100)
			if err != nil {
				return err
			}
			if reset {
				fmt.Fprint(w, "event: reset\ndata: {}\n\n")
				flusher.Flush()
				last, err = h.svc.Latest(ctx, ch.ID)
				return err
			}
			for _, e := range evs {
				fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, e.Payload)
				last = e.ID
			}
			if len(evs) > 0 {
				flusher.Flush()
			}
			if len(evs) < 100 {
				return nil
			}
		}
	}
	if last == 0 { // a fresh stream starts now
		var err error
		if last, err = h.svc.Latest(ctx, ch.ID); err != nil {
			h.logger.Warn().Err(err).Msg("channel events")
			return nil
		}
	}
	if err := send(); err != nil {
		h.logger.Warn().Err(err).Msg("channel events")
		return nil
	}
	poll := time.NewTicker(2 * time.Second) // other main instances' events
	defer poll.Stop()
	keep := time.NewTicker(15 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-keep.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-wake:
		case <-poll.C:
		}
		if err := send(); err != nil {
			h.logger.Warn().Err(err).Msg("channel events")
			return nil
		}
	}
}

// GET /track/v1/:token — a customer's tracking link (no key).
func (h *TrackingHandler) Public(c *echo.Context) error {
	ip := c.RealIP()
	if !h.public.Allow(ip) {
		c.Response().Header().Set("Retry-After", "30")
		return c.JSON(http.StatusTooManyRequests, errCode("too many requests", "too_many_requests"))
	}
	h.public.Add(ip)
	c.Response().Header().Set("Cache-Control", "no-store")
	t, err := h.svc.ByToken(c.Request().Context(), c.Param("token"))
	if errors.Is(err, tracking.ErrNotFound) {
		return c.JSON(http.StatusNotFound, errCode("no such order", "not_found"))
	}
	if err != nil {
		h.logger.Error().Err(err).Msg("public tracking")
		return c.JSON(http.StatusInternalServerError, errCode("tracking unavailable", "internal"))
	}
	return c.JSON(http.StatusOK, t)
}
