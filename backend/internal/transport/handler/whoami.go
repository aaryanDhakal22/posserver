package handler

import (
	"net/http"

	authSvc "quiccpos/main/internal/app/auth"
	tenancyApp "quiccpos/main/internal/app/tenancy"
	domainAuth "quiccpos/main/internal/domain/auth"

	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// WhoamiHandler answers the Pi's "is my key still good?" (hardening F-4):
// the Pi forgets its link only on main's explicit device_revoked, confirmed
// here twice ≥ 60 s apart; any other 401 (a proxy, a route bug, a main
// restored from an older backup) is retried and never wipes the store.
type WhoamiHandler struct {
	auth    *authSvc.Service
	tenancy *tenancyApp.Service
	logger  zerolog.Logger
}

func NewWhoamiHandler(a *authSvc.Service, t *tenancyApp.Service, lg zerolog.Logger) *WhoamiHandler {
	return &WhoamiHandler{auth: a, tenancy: t, logger: lg.With().Str("module", "whoami").Logger()}
}

// GET /store/v1/agent/whoami (X-STORE-API-KEY): 200 {device_id, agent_id,
// kind, tenant_id, location_id, tenant, location, revoked: false} · 401
// {error, code: device_revoked, revoked: true, device_id} for a revoked /
// deleted key · 401 {error, code: unknown_key, revoked: false} for a key main
// has no record of · 401 {code: missing_key} without the header.
func (h *WhoamiHandler) Whoami(c *echo.Context) error {
	ctx := c.Request().Context()
	key := c.Request().Header.Get("X-STORE-API-KEY")
	if key == "" {
		return c.JSON(http.StatusUnauthorized, map[string]any{"error": "missing X-STORE-API-KEY header", "code": "missing_key", "revoked": false})
	}
	dev, err := h.auth.Authenticate(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		state, id, serr := h.auth.KeyState(ctx, key)
		if serr != nil {
			h.logger.Error().Ctx(ctx).Err(serr).Msg("whoami: key state")
			return c.JSON(http.StatusInternalServerError, map[string]any{"error": "auth check failed", "code": "internal"})
		}
		out := map[string]any{"error": "invalid API key", "code": state, "revoked": state == domainAuth.KeyRevoked}
		if id != nil {
			out["device_id"] = id.String()
		}
		h.logger.Warn().Ctx(ctx).Str("code", state).Msg("whoami: key not accepted")
		return c.JSON(http.StatusUnauthorized, out)
	}
	if err != nil {
		h.logger.Error().Ctx(ctx).Err(err).Msg("whoami: lookup")
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "auth check failed", "code": "internal"})
	}
	out := map[string]any{"device_id": dev.ID.String(), "agent_id": dev.ID.String(), "kind": dev.Kind,
		"tenant_id": dev.TenantID.String(), "location_id": dev.LocationID.String(), "tenant": "", "location": "", "revoked": false}
	if t, err := h.tenancy.GetTenant(ctx, dev.TenantID); err == nil {
		out["tenant"] = t.Name
	}
	if l, err := h.tenancy.GetLocation(ctx, dev.LocationID); err == nil {
		out["location"] = l.Name
	}
	return c.JSON(http.StatusOK, out)
}
