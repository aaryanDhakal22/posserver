package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"quiccpos/main/internal/app/geo"
	storestatusApp "quiccpos/main/internal/app/storestatus"
	"quiccpos/main/internal/domain/storestatus"
	"quiccpos/main/internal/shared/ratelimit"
	appMiddleware "quiccpos/main/internal/transport/middleware"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// StoreStatusHandler serves a store's ordering status (main 3.0 step 3): the
// manager's /store/v1/me/{status,ordering,pause,hours-exceptions} (session)
// and the channels' /channel/v1/store[/times,/delivery-check] (channel key).
type StoreStatusHandler struct {
	svc    *storestatusApp.Service
	appEnv string
	checks *ratelimit.Window // delivery checks per channel
	logger zerolog.Logger
}

// deliveryChecksPerMinute bounds geocoding per channel.
const deliveryChecksPerMinute = 30

func NewStoreStatusHandler(svc *storestatusApp.Service, appEnv string, logger zerolog.Logger) *StoreStatusHandler {
	return &StoreStatusHandler{svc: svc, appEnv: appEnv, checks: ratelimit.New(deliveryChecksPerMinute, time.Minute),
		logger: logger.With().Str("module", "storestatus-handler").Logger()}
}

func (h *StoreStatusHandler) sErr(c *echo.Context, err error) error {
	switch {
	case errors.Is(err, storestatusApp.ErrNotFound):
		return c.JSON(http.StatusNotFound, errCode(err.Error(), "not_found"))
	case errors.Is(err, storestatus.ErrTypeOff):
		return c.JSON(http.StatusConflict, errCode(err.Error(), "type_off"))
	case errors.Is(err, storestatus.ErrScheduleOff):
		return c.JSON(http.StatusConflict, errCode(err.Error(), "asap_only"))
	case errors.Is(err, storestatusApp.ErrNoteTooLong), errors.Is(err, storestatusApp.ErrBadAddress),
		errors.Is(err, storestatus.ErrBadSettings), errors.Is(err, storestatus.ErrBadType), errors.Is(err, storestatus.ErrBadDate),
		errors.Is(err, storestatus.ErrBadPause), errors.Is(err, storestatus.ErrBadException):
		return c.JSON(http.StatusBadRequest, errCode(err.Error(), "bad_request"))
	}
	h.logger.Error().Err(err).Msg("store status")
	return c.JSON(http.StatusInternalServerError, errCode("store status failed", "internal"))
}

// --- the manager -------------------------------------------------------------------

// GET /store/v1/me/status — the status channels see.
func (h *StoreStatusHandler) MeStatus(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	out, err := h.svc.Status(c.Request().Context(), tid, lid)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

// GET /store/v1/me/ordering — settings (incl. the pause) + hours exceptions.
func (h *StoreStatusHandler) MeOrdering(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	ctx := c.Request().Context()
	st, err := h.svc.Settings(ctx, tid, lid)
	if err != nil {
		return h.sErr(c, err)
	}
	exc, err := h.svc.Exceptions(ctx, tid, lid)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"settings": st, "exceptions": exc})
}

// PUT /store/v1/me/ordering {pickup_enabled, delivery_enabled, pickup_eta_minutes, delivery_eta_minutes, schedule_days}
func (h *StoreStatusHandler) MeUpdateOrdering(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	var req storestatus.Settings
	if err := echo.BindBody(c, &req); err != nil {
		return badBody(c)
	}
	st, err := h.svc.UpdateSettings(c.Request().Context(), tid, lid, req)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, st)
}

// POST /store/v1/me/pause {preset: 15m|30m|60m|today|indefinite, note}
func (h *StoreStatusHandler) MePause(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	var req struct {
		Preset string `json:"preset"`
		Note   string `json:"note"`
	}
	if err := echo.BindBody(c, &req); err != nil {
		return badBody(c)
	}
	st, err := h.svc.Pause(c.Request().Context(), tid, lid, req.Preset, req.Note)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, st)
}

// DELETE /store/v1/me/pause
func (h *StoreStatusHandler) MeResume(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	st, err := h.svc.Resume(c.Request().Context(), tid, lid)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, st)
}

// POST /store/v1/me/hours-exceptions {date, closed, hours, note} — adds the
// date's exception or replaces the one it has.
func (h *StoreStatusHandler) MeSetException(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	var req storestatus.Exception
	if err := echo.BindBody(c, &req); err != nil {
		return badBody(c)
	}
	e, err := h.svc.SetException(c.Request().Context(), tid, lid, req)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, e)
}

func (h *StoreStatusHandler) MeDeleteException(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	if err := h.svc.DeleteException(c.Request().Context(), lid, id); err != nil {
		return h.sErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// POST /admin/v1/store-status/restock {location_id} — DEV ONLY: run a
// location's restock now (404 outside dev).
func (h *StoreStatusHandler) DevRestock(c *echo.Context) error {
	if h.appEnv != "dev" {
		return c.JSON(http.StatusNotFound, errResp("not found"))
	}
	var req struct {
		LocationID uuid.UUID `json:"location_id"`
	}
	if err := echo.BindBody(c, &req); err != nil || req.LocationID == uuid.Nil {
		return c.JSON(http.StatusBadRequest, errResp("location_id required"))
	}
	n, err := h.svc.RestockNow(c.Request().Context(), req.LocationID)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"back": n})
}

// --- /channel/v1/store -------------------------------------------------------------

// GET /channel/v1/store — the store's status (any active channel).
func (h *StoreStatusHandler) ChannelStore(c *echo.Context) error {
	ch, ok := appMiddleware.ChannelFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, errCode("missing channel key", "unauthorized"))
	}
	out, err := h.svc.Status(c.Request().Context(), ch.TenantID, ch.LocationID)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

// GET /channel/v1/store/times?type=pickup|delivery&date=YYYY-MM-DD
func (h *StoreStatusHandler) ChannelTimes(c *echo.Context) error {
	ch, ok := appMiddleware.ChannelFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, errCode("missing channel key", "unauthorized"))
	}
	kind, date := c.QueryParam("type"), c.QueryParam("date")
	times, err := h.svc.Times(c.Request().Context(), ch.TenantID, ch.LocationID, kind, date)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"type": kind, "date": date, "slot_minutes": storestatus.SlotMinutes, "times": times})
}

// POST /channel/v1/store/delivery-check {line1, city, state, zip}
func (h *StoreStatusHandler) ChannelDeliveryCheck(c *echo.Context) error {
	ch, ok := appMiddleware.ChannelFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, errCode("missing channel key", "unauthorized"))
	}
	var req struct {
		Line1 string `json:"line1"`
		City  string `json:"city"`
		State string `json:"state"`
		Zip   string `json:"zip"`
	}
	if err := echo.BindBody(c, &req); err != nil {
		return badBody(c)
	}
	key := ch.ID.String()
	if !h.checks.Allow(key) {
		secs := int(h.checks.RetryAfter(key).Seconds()) + 1
		c.Response().Header().Set("Retry-After", strconv.Itoa(secs))
		return c.JSON(http.StatusTooManyRequests, errCode("too many delivery checks, try again shortly", "too_many_requests"))
	}
	h.checks.Add(key)
	out, err := h.svc.DeliveryCheck(c.Request().Context(), ch.TenantID, ch.LocationID,
		geo.Address{Line1: req.Line1, City: req.City, State: req.State, Zip: req.Zip})
	if err != nil {
		if errors.Is(err, storestatusApp.ErrBadAddress) {
			return h.sErr(c, err)
		}
		h.logger.Warn().Err(err).Msg("delivery check: geocoder failed") // never the address
		return c.JSON(http.StatusServiceUnavailable, errCode("address lookup is unavailable, try again", "try_again"))
	}
	return c.JSON(http.StatusOK, out)
}
