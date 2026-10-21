package handler

import (
	"errors"
	"net/http"
	"time"

	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"
	appMiddleware "quiccpos/main/internal/transport/middleware"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// AgentRPCHandler is the single agent→main "up" endpoint of the duplex channel.
// It receives REPLIES (matched to a pending Requester.Request by correlation id)
// and unsolicited REPORTS (dispatched to a registered inbound handler).
type AgentRPCHandler struct {
	requester *rpc.Requester
	logger    zerolog.Logger
}

func NewAgentRPCHandler(requester *rpc.Requester, logger zerolog.Logger) *AgentRPCHandler {
	return &AgentRPCHandler{requester: requester, logger: logger.With().Str("module", "agent-rpc").Logger()}
}

// Handle serves POST /store/v1/agent/rpc — device-authed (X-STORE-API-KEY); the
// scope comes from the authenticated device, so an agent can only reply/report
// for its own (tenant, location).
func (h *AgentRPCHandler) Handle(c *echo.Context) error {
	dev := appMiddleware.DeviceFromContext(c.Request().Context())
	if dev == nil {
		return c.JSON(http.StatusUnauthorized, errResp("unauthorized"))
	}
	scope := sseBroker.Scope{TenantID: dev.TenantID, LocationID: dev.LocationID}

	var env rpc.Envelope
	if err := c.Bind(&env); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid envelope"))
	}

	// A reply to a pending main→agent request?
	if h.requester.Deliver(scope, env) {
		return c.NoContent(http.StatusNoContent)
	}
	// Otherwise an agent-initiated report/request.
	reply, handled, err := h.requester.DispatchInbound(c.Request().Context(), scope, env)
	if errors.Is(err, rpc.ErrBadPayload) {
		h.logger.Warn().Err(err).Str("type", env.Type).Msg("inbound rpc payload rejected")
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	if err != nil {
		h.logger.Error().Err(err).Str("type", env.Type).Msg("inbound rpc handler failed")
		return c.JSON(http.StatusInternalServerError, errResp("handler failed"))
	}
	if !handled {
		return c.NoContent(http.StatusAccepted) // unknown type — forward-compat no-op
	}
	if reply.ID == "" {
		return c.NoContent(http.StatusNoContent) // fire-and-forget
	}
	return c.JSON(http.StatusOK, reply)
}

// PingAgent serves POST /admin/v1/agent/ping — admin debug: round-trip an agent
// over the duplex channel to prove main→agent (SSE) + agent→main (POST) +
// correlation. Body: {tenant_id, location_id}.
func (h *AgentRPCHandler) PingAgent(c *echo.Context) error {
	var req struct {
		TenantID   string `json:"tenant_id"`
		LocationID string `json:"location_id"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	tid, err1 := uuid.Parse(req.TenantID)
	lid, err2 := uuid.Parse(req.LocationID)
	if err1 != nil || err2 != nil {
		return c.JSON(http.StatusBadRequest, errResp("tenant_id and location_id must be uuids"))
	}
	scope := sseBroker.Scope{TenantID: tid, LocationID: lid}

	reply, err := h.requester.Request(c.Request().Context(), scope, rpc.MsgPing, nil, 3*time.Second)
	if err != nil {
		switch {
		case errors.Is(err, rpc.ErrAgentOffline):
			return c.JSON(http.StatusServiceUnavailable, errResp("agent offline"))
		case errors.Is(err, rpc.ErrTimeout):
			return c.JSON(http.StatusGatewayTimeout, errResp("agent did not reply in time"))
		default:
			return c.JSON(http.StatusInternalServerError, errResp(err.Error()))
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"type": reply.Type, "payload": reply.Payload})
}
