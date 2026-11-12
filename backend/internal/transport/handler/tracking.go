package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"quiccpos/main/internal/app/tracking"
	channelDom "quiccpos/main/internal/domain/channel"
	siteDom "quiccpos/main/internal/domain/site"
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
	public *ratelimit.Window // per customer IP
	site   *ratelimit.Window // per storefront (platform key), all its customers together
	// siteAuth verifies a storefront's platform key + X-Site-Host (channelApp.Service.AuthenticateSite).
	// Only then is its X-Customer-IP believed; nil = never (tests, or no channel service).
	siteAuth func(ctx context.Context, key, host string) (*channelDom.Channel, error)
	logger   zerolog.Logger
	// dev only (POST /admin/v1/dev/track-limit): a storefront's own, lower per-customer limit, so a
	// test can prove the limit is per customer without tripping everyone else's on dev main
	appEnv    string
	mu        sync.Mutex
	overrides map[string]*ratelimit.Window // storefront key id → per-customer window
}

// TrackPublicPerMinDefault is the /track/v1 limit per IP and minute (staging and prod).
const TrackPublicPerMinDefault = 120

// TrackSitePerMinFactor: one storefront (all of its customers, after their own per-IP limits) may make
// this many times the per-IP limit a minute — a ceiling on a storefront gone wrong, not on customers.
const TrackSitePerMinFactor = 50

// TrackPublicPerMin is the /track/v1 limit per IP and minute: TRACK_PUBLIC_PER_MIN when that is a
// positive integer, else TrackPublicPerMinDefault. Only the dev compose (and the gate's stacks) set
// it: the browser suites open tracking pages from one machine, faster than 120 a minute.
func TrackPublicPerMin() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("TRACK_PUBLIC_PER_MIN"))); err == nil && n > 0 {
		return n
	}
	return TrackPublicPerMinDefault
}

// NewTrackingHandler: perMin ≤ 0 means TrackPublicPerMinDefault.
func NewTrackingHandler(svc *tracking.Service, perMin int, logger zerolog.Logger) *TrackingHandler {
	if perMin <= 0 {
		perMin = TrackPublicPerMinDefault
	}
	return &TrackingHandler{svc: svc, public: ratelimit.New(perMin, time.Minute),
		site:   ratelimit.New(perMin*TrackSitePerMinFactor, time.Minute),
		logger: logger.With().Str("module", "tracking-handler").Logger()}
}

// SetSiteAuth lets /track/v1 recognise the storefront (wired in routes.go).
func (h *TrackingHandler) SetSiteAuth(f func(ctx context.Context, key, host string) (*channelDom.Channel, error)) {
	h.siteAuth = f
}

// trackCaller is who a /track/v1 lookup counts against: the customer's IP when a verified storefront
// forwards it (X-Customer-IP, from a request carrying its platform key + X-Site-Host), else the
// caller's own address. site is the storefront's key id ("" when not a verified storefront).
// A header without a valid key is ignored, so nobody escapes the limit by setting it.
func (h *TrackingHandler) trackCaller(c *echo.Context) (ip, site string) {
	ip = c.RealIP()
	r := c.Request()
	key := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if h.siteAuth == nil || !strings.HasPrefix(key, siteDom.KeyPrefix) {
		return ip, ""
	}
	ch, err := h.siteAuth(r.Context(), key, r.Header.Get("X-Site-Host"))
	if err != nil || ch == nil || ch.Kind != channelDom.KindWeb {
		return ip, ""
	}
	sum := sha256.Sum256([]byte(key))
	site = hex.EncodeToString(sum[:8])
	if cust := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Customer-IP"))); cust != nil {
		return "cust|" + cust.String(), site
	}
	return ip, site // a verified storefront that didn't say who: its own address, as before
}

// admit counts one /track/v1 lookup; false = over the customer's or the storefront's limit.
func (h *TrackingHandler) admit(c *echo.Context) bool {
	ip, site := h.trackCaller(c)
	win := h.public
	if site != "" {
		h.mu.Lock()
		if o := h.overrides[site]; o != nil {
			win = o
		}
		h.mu.Unlock()
	}
	if !win.Allow(ip) || (site != "" && !h.site.Allow(site)) {
		return false
	}
	win.Add(ip)
	if site != "" {
		h.site.Add(site)
	}
	return true
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

// SetDev enables the dev-only hook (appEnv "dev").
func (h *TrackingHandler) SetDev(appEnv string) { h.appEnv = appEnv }

// POST /admin/v1/dev/track-limit {key, per_min} (dev only): this storefront's customers get their own
// per-minute /track/v1 limit (0 = back to the shared one). Only that platform key is affected.
func (h *TrackingHandler) DevTrackLimit(c *echo.Context) error {
	if h.appEnv != "dev" {
		return c.JSON(http.StatusNotFound, errCode("not found", "not_found"))
	}
	var b struct {
		Key    string `json:"key"`
		PerMin int    `json:"per_min"`
	}
	if err := echo.BindBody(c, &b); err != nil || !strings.HasPrefix(b.Key, siteDom.KeyPrefix) || b.PerMin < 0 {
		return c.JSON(http.StatusBadRequest, errCode("send {key: a qpk_ platform key, per_min ≥ 0}", "bad_request"))
	}
	sum := sha256.Sum256([]byte(b.Key))
	site := hex.EncodeToString(sum[:8])
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.overrides == nil {
		h.overrides = map[string]*ratelimit.Window{}
	}
	if b.PerMin == 0 {
		delete(h.overrides, site)
	} else {
		h.overrides[site] = ratelimit.New(b.PerMin, time.Minute)
	}
	return c.JSON(http.StatusOK, map[string]int{"per_min": b.PerMin})
}

// GET /track/v1/:token — a customer's tracking link (no key).
// The limit is per customer (trackCaller), plus a ceiling per storefront.
func (h *TrackingHandler) Public(c *echo.Context) error {
	if !h.admit(c) {
		c.Response().Header().Set("Retry-After", "30")
		return c.JSON(http.StatusTooManyRequests, errCode("too many requests", "too_many_requests"))
	}
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
