package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"quiccpos/main/internal/app/agentsync"
	"quiccpos/main/internal/infra/database/models"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// DevicesHandler serves Store › Devices (contract main §2.5zk, agent 2.5.3):
// the store's POS terminals and Quibase tablets as its Pi last reported them
// (agent.health), the newest release main has per app, and "Update now".
type DevicesHandler struct {
	sync      *agentsync.Service
	pool      *pgxpool.Pool
	requester *rpc.Requester
	logger    zerolog.Logger
}

func NewDevicesHandler(sync *agentsync.Service, pool *pgxpool.Pool, requester *rpc.Requester, logger zerolog.Logger) *DevicesHandler {
	return &DevicesHandler{sync: sync, pool: pool, requester: requester, logger: logger.With().Str("module", "devices-handler").Logger()}
}

// UpdateNowTimeout: the Pi may download the release first (tens of MB).
const UpdateNowTimeout = 45 * time.Second

func (h *DevicesHandler) latest(c *echo.Context, app string) *AppRelease {
	if h.pool == nil {
		return nil
	}
	r, err := models.New(h.pool).GetLatestAppRelease(c.Request().Context(), app)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			h.logger.Warn().Err(err).Str("app", app).Msg("latest app release lookup failed")
		}
		return nil
	}
	return &AppRelease{App: r.App, VersionCode: r.VersionCode, VersionName: r.VersionName, SHA256: r.Sha256, Size: r.Size,
		CreatedAt: r.CreatedAt.Time}
}

// MeDevices serves GET /store/v1/me/terminals → {connected, reported_at,
// terminals[], tablets[], releases: {terminal, quibase}}.
func (h *DevicesHandler) MeDevices(c *echo.Context) error {
	_, lid, ok := meScope(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, errResp("no store session"))
	}
	out := map[string]any{"connected": false, "reported_at": nil, "terminals": json.RawMessage(`[]`), "tablets": json.RawMessage(`[]`),
		"releases": map[string]any{"terminal": h.latest(c, "terminal"), "quibase": h.latest(c, "quibase")}}
	if h.sync != nil {
		ah, err := h.sync.LocationHealth(c.Request().Context(), lid)
		if err != nil {
			h.logger.Error().Ctx(c.Request().Context()).Err(err).Msg("devices: health read failed")
			return c.JSON(http.StatusInternalServerError, errResp("devices unavailable"))
		}
		if ah != nil {
			out["connected"], out["reported_at"], out["terminals"], out["tablets"] = ah.Connected, ah.At, ah.Terminals, ah.Tablets
		}
	}
	return c.JSON(http.StatusOK, out)
}

// MeUpdateTerminals serves POST /store/v1/me/terminals/update-now (manager
// session or an admin's store session; audited): RPC terminals.update_now to
// the store's Pi → its reply {ok, code?, release, terminals[]} · 503
// agent_offline · 504 timeout (an agent older than 2.5.3 never answers).
func (h *DevicesHandler) MeUpdateTerminals(c *echo.Context) error {
	tid, lid, ok := meScope(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, errResp("no store session"))
	}
	if h.requester == nil {
		return c.JSON(http.StatusServiceUnavailable, errCode("the Pi is not connected", "agent_offline"))
	}
	scope := sseBroker.Scope{TenantID: tid, LocationID: lid}
	reply, err := h.requester.Request(c.Request().Context(), scope, rpc.MsgTerminalsUpdateNow, map[string]any{"requested_by": "store"}, UpdateNowTimeout)
	switch {
	case errors.Is(err, rpc.ErrAgentOffline):
		return c.JSON(http.StatusServiceUnavailable, errCode("The Pi is not connected: try again when it is online", "agent_offline"))
	case errors.Is(err, rpc.ErrTimeout):
		return c.JSON(http.StatusGatewayTimeout, errCode("The Pi did not answer (it may run an agent older than 2.5.3)", "timeout"))
	case err != nil:
		h.logger.Error().Ctx(c.Request().Context()).Err(err).Msg("terminals update now failed")
		return c.JSON(http.StatusInternalServerError, errResp("update request failed"))
	}
	h.logger.Info().Ctx(c.Request().Context()).Str("location_id", lid.String()).Msg("terminals told to update now")
	return c.JSONBlob(http.StatusOK, reply.Payload)
}
