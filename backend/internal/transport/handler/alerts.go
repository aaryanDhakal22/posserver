package handler

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/infra/pushover"
	"quiccpos/main/internal/shared/ratelimit"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// TestAlertMessage is the one Pushover the owner's Send test sends.
const TestAlertMessage = "Test alert from QuiccPOS — floor problems will arrive like this."

// AlertSender is the slice of the Pushover client the alert test uses.
type AlertSender interface {
	Enabled() bool
	SendDevices(ctx context.Context, devices []string, title, message, sound string) error
}

// LocationLoader loads a location (the tenancy service).
type LocationLoader interface {
	GetLocation(ctx context.Context, id uuid.UUID) (*tenancy.Location, error)
}

// AlertsHandler backs the store owner's alert phones on Store › Details: the
// device names are saved in the location config (PATCH /me/location); this
// sends one test Pushover to them from main.
type AlertsHandler struct {
	locs   LocationLoader
	sender AlertSender
	limit  *ratelimit.Window
	logger zerolog.Logger
}

func NewAlertsHandler(locs LocationLoader, sender AlertSender, logger zerolog.Logger) *AlertsHandler {
	return &AlertsHandler{
		locs:   locs,
		sender: sender,
		limit:  ratelimit.New(1, 10*time.Second),
		logger: logger.With().Str("module", "alerts-handler").Logger(),
	}
}

// locationErr is a location save's 400 body; a bad Pushover device name gets
// code bad_device.
func locationErr(err error) map[string]string {
	var bad *tenancy.BadDeviceError
	if errors.As(err, &bad) {
		return errCode(err.Error(), "bad_device")
	}
	return errResp(err.Error())
}

// POST /store/v1/me/alerts/test — one Pushover to the saved owner devices.
// 200 {ok} · 400 no_devices / device_not_found · 429 too_soon (1 per 10 s per
// location) · 502 pushover_rejected / pushover_unreachable · 503 not_configured.
func (h *AlertsHandler) MeTest(c *echo.Context) error {
	ctx := c.Request().Context()
	_, lid, _ := meScope(c)
	loc, err := h.locs.GetLocation(ctx, lid)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("could not load the location"))
	}
	devices := tenancy.OwnerDevices(loc.Config)
	if len(devices) == 0 {
		return c.JSON(http.StatusBadRequest, errCode("No phones saved yet — add one and save first.", "no_devices"))
	}
	if h.sender == nil || !h.sender.Enabled() {
		return c.JSON(http.StatusServiceUnavailable, errCode("Pushover is not set up on the server.", "not_configured"))
	}
	key := lid.String()
	if !h.limit.Allow(key) {
		secs := int(math.Ceil(h.limit.RetryAfter(key).Seconds()))
		c.Response().Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
		return c.JSON(http.StatusTooManyRequests, errCode("Wait a few seconds before sending another test.", "too_soon"))
	}
	h.limit.Add(key)

	err = h.sender.SendDevices(ctx, devices, "", TestAlertMessage, "")
	log := h.logger.Info().Ctx(ctx).Str("location_id", key).Int("devices", len(devices))
	var rej *pushover.RejectedError
	switch {
	case err == nil:
		log.Msg("alert test sent")
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	case errors.Is(err, pushover.ErrNotConfigured):
		return c.JSON(http.StatusServiceUnavailable, errCode("Pushover is not set up on the server.", "not_configured"))
	case errors.As(err, &rej) && rej.DeviceNotFound():
		log.Int("status", rej.Status).Msg("alert test: device not found")
		return c.JSON(http.StatusBadRequest, errCode(
			"Pushover doesn't know one of these phones — check the device names in the Pushover app.", "device_not_found"))
	case errors.As(err, &rej) && rej.Status < 500:
		log.Int("status", rej.Status).Strs("errors", rej.Errors).Msg("alert test rejected")
		return c.JSON(http.StatusBadGateway, errCode("Pushover refused the test alert.", "pushover_rejected"))
	default:
		h.logger.Warn().Ctx(ctx).Err(err).Str("location_id", key).Msg("alert test: pushover unreachable")
		return c.JSON(http.StatusBadGateway, errCode("Couldn't reach Pushover — try again in a minute.", "pushover_unreachable"))
	}
}
