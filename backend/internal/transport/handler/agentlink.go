package handler

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"quiccpos/main/internal/app/agentsync"
	sseBroker "quiccpos/main/internal/infra/sse"
	appMiddleware "quiccpos/main/internal/transport/middleware"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// AgentLinkHandler serves the headless-recovery surface: a blank agent links
// with the store manager login, restores the open batch, uploads nightly
// backups; admins read fleet health + backups.
type AgentLinkHandler struct {
	svc    *agentsync.Service
	logger zerolog.Logger
}

func NewAgentLinkHandler(svc *agentsync.Service, logger zerolog.Logger) *AgentLinkHandler {
	return &AgentLinkHandler{svc: svc, logger: logger.With().Str("module", "agent-link-handler").Logger()}
}

// Link — POST /store/v1/agent/link {username, password, agent_name} (no key:
// the manager login is the credential). 401 bad login, 429 throttled.
func (h *AgentLinkHandler) Link(c *echo.Context) error {
	var req agentsync.LinkRequest
	if err := echo.BindBody(c, &req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	req.RemoteAddr = c.RealIP()
	res, err := h.svc.Link(c.Request().Context(), req)
	switch {
	case errors.Is(err, agentsync.ErrThrottled):
		return c.JSON(http.StatusTooManyRequests, map[string]string{"error": err.Error(), "code": "too_many_attempts"})
	case errors.Is(err, agentsync.ErrBadCredentials):
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": err.Error(), "code": "invalid_credentials"})
	case err != nil:
		h.logger.Error().Ctx(c.Request().Context()).Err(err).Msg("agent link failed")
		return c.JSON(http.StatusInternalServerError, errResp("link failed: "+err.Error()))
	}
	return c.JSON(http.StatusOK, res)
}

func agentScope(c *echo.Context) (sseBroker.Scope, uuid.UUID, bool) {
	dev := appMiddleware.DeviceFromContext(c.Request().Context())
	if dev == nil {
		return sseBroker.Scope{}, uuid.Nil, false
	}
	return sseBroker.Scope{TenantID: dev.TenantID, LocationID: dev.LocationID}, dev.ID, true
}

// Restore — GET /store/v1/agent/restore (agent key): the open batch for a
// replacement agent.
func (h *AgentLinkHandler) Restore(c *echo.Context) error {
	scope, _, ok := agentScope(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, errResp("device required"))
	}
	res, err := h.svc.Restore(c.Request().Context(), scope)
	if err != nil {
		h.logger.Error().Ctx(c.Request().Context()).Err(err).Msg("restore failed")
		return c.JSON(http.StatusInternalServerError, errResp("restore failed"))
	}
	return c.JSON(http.StatusOK, res)
}

// Credential — GET /store/v1/agent/credential (agent key): the current
// manager login hash (offline web-UI unlock).
func (h *AgentLinkHandler) Credential(c *echo.Context) error {
	scope, _, ok := agentScope(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, errResp("device required"))
	}
	m, err := h.svc.Credential(c.Request().Context(), scope.TenantID)
	if err != nil {
		return c.JSON(http.StatusNotFound, errResp("no manager login set for this store"))
	}
	return c.JSON(http.StatusOK, m)
}

// Backup — POST /store/v1/agent/backup (agent key, body = SQLite file).
func (h *AgentLinkHandler) Backup(c *echo.Context) error {
	scope, devID, ok := agentScope(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, errResp("device required"))
	}
	data, err := io.ReadAll(io.LimitReader(c.Request().Body, agentsync.MaxBackup+1))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("could not read the upload"))
	}
	if len(data) > agentsync.MaxBackup {
		return c.JSON(http.StatusRequestEntityTooLarge, errResp("backup too large"))
	}
	b, err := h.svc.StoreBackup(c.Request().Context(), scope, devID, data)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	return c.JSON(http.StatusCreated, b)
}

// Fleet — GET /admin/v1/agents/health: every location's agent health.
func (h *AgentLinkHandler) Fleet(c *echo.Context) error {
	out, err := h.svc.Fleet(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("fleet failed"))
	}
	return c.JSON(http.StatusOK, out)
}

// ListBackups — GET /admin/v1/agent-backups?location_id=
func (h *AgentLinkHandler) ListBackups(c *echo.Context) error {
	loc := uuid.Nil
	if raw := strings.TrimSpace(c.QueryParam("location_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return c.JSON(http.StatusBadRequest, errResp("location_id must be a uuid"))
		}
		loc = id
	}
	out, err := h.svc.ListBackups(c.Request().Context(), loc)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("list failed"))
	}
	return c.JSON(http.StatusOK, out)
}

// GetBackup — GET /admin/v1/agent-backups/:id → the SQLite file.
func (h *AgentLinkHandler) GetBackup(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	data, at, err := h.svc.BackupData(c.Request().Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusNotFound, errResp("no such backup"))
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("read failed"))
	}
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="agent-`+at.UTC().Format("20060102-150405")+`.db"`)
	return c.Blob(http.StatusOK, "application/vnd.sqlite3", data)
}
