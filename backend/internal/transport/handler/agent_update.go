package handler

import (
	"errors"
	"net/http"
	"strconv"

	"quiccpos/main/internal/app/agentsync"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// AgentUpdateHandler serves the admin Pi-update surface: ask a location's Pi
// to update / roll back (over the duplex channel) and read the update history.
type AgentUpdateHandler struct {
	svc       *agentsync.Service
	requester agentsync.Commander
	logger    zerolog.Logger
}

func NewAgentUpdateHandler(svc *agentsync.Service, requester agentsync.Commander, logger zerolog.Logger) *AgentUpdateHandler {
	return &AgentUpdateHandler{svc: svc, requester: requester, logger: logger.With().Str("module", "agent-update-handler").Logger()}
}

// Update serves POST /admin/v1/agent/update {tenant_id, location_id, force,
// requested_by?}: 200 {id, accepted, state, code}; 503 no Pi connected; 504 no
// reply in time. The request is recorded in agent_updates either way.
func (h *AgentUpdateHandler) Update(c *echo.Context) error {
	return h.request(c, agentsync.UpdateActionUpdate)
}

// Rollback serves POST /admin/v1/agent/rollback {tenant_id, location_id,
// requested_by?} — same responses as Update.
func (h *AgentUpdateHandler) Rollback(c *echo.Context) error {
	return h.request(c, agentsync.UpdateActionRollback)
}

func (h *AgentUpdateHandler) request(c *echo.Context, action string) error {
	var req struct {
		TenantID    string `json:"tenant_id"`
		LocationID  string `json:"location_id"`
		Force       bool   `json:"force"`
		RequestedBy string `json:"requested_by"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	tid, err1 := uuid.Parse(req.TenantID)
	lid, err2 := uuid.Parse(req.LocationID)
	if err1 != nil || err2 != nil {
		return c.JSON(http.StatusBadRequest, errResp("tenant_id and location_id must be uuids"))
	}
	switch req.RequestedBy {
	case "":
		req.RequestedBy = "admin"
	case "admin", "console":
	default:
		return c.JSON(http.StatusBadRequest, errResp(`requested_by must be "admin" or "console"`))
	}
	res, err := h.svc.RequestUpdate(c.Request().Context(), h.requester, agentsync.UpdateRequest{
		TenantID: tid, LocationID: lid, Action: action, Force: req.Force, RequestedBy: req.RequestedBy,
	})
	switch {
	case errors.Is(err, agentsync.ErrUnknownLocation):
		return c.JSON(http.StatusNotFound, errResp("no such location for that tenant"))
	case errors.Is(err, rpc.ErrAgentOffline):
		return c.JSON(http.StatusServiceUnavailable, map[string]any{
			"error": "the Pi is not connected", "code": "agent_offline", "id": res.ID, "state": res.State,
		})
	case errors.Is(err, rpc.ErrTimeout):
		return c.JSON(http.StatusGatewayTimeout, map[string]any{
			"error": "the Pi did not answer — it may run an agent older than 2.0.6", "code": "timeout", "id": res.ID, "state": res.State,
		})
	case err != nil && res.ID != "":
		return c.JSON(http.StatusServiceUnavailable, map[string]any{
			"error": err.Error(), "code": "agent_offline", "id": res.ID, "state": res.State,
		})
	case err != nil:
		h.logger.Error().Ctx(c.Request().Context()).Err(err).Msg("pi update request failed")
		return c.JSON(http.StatusInternalServerError, errResp("update request failed"))
	}
	return c.JSON(http.StatusOK, res)
}

// List serves GET /admin/v1/agent-updates?location_id=&limit=20 — update
// history, newest first (every location when location_id is absent).
func (h *AgentUpdateHandler) List(c *echo.Context) error {
	lid := uuid.Nil
	if raw := c.QueryParam("location_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return c.JSON(http.StatusBadRequest, errResp("location_id must be a uuid"))
		}
		lid = id
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	rows, err := h.svc.ListUpdates(c.Request().Context(), lid, limit)
	if err != nil {
		h.logger.Error().Ctx(c.Request().Context()).Err(err).Msg("list pi updates failed")
		return c.JSON(http.StatusInternalServerError, errResp("list failed"))
	}
	return c.JSON(http.StatusOK, rows)
}
