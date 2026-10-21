package handler

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"quiccpos/main/internal/app/diag"
	appMiddleware "quiccpos/main/internal/transport/middleware"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// DiagHandler serves tablet diagnostics: report tokens minted through the
// agent, the tablet's direct report upload (X-Quibase-Report-Key), and the
// admin list / detail / resolve + token management. Reports relayed by the Pi
// arrive as the RPC report diag.report (diag.Service.HandleRPC), not here.
type DiagHandler struct {
	svc    *diag.Service
	logger zerolog.Logger
}

func NewDiagHandler(svc *diag.Service, logger zerolog.Logger) *DiagHandler {
	return &DiagHandler{svc: svc, logger: logger.With().Str("module", "diag-handler").Logger()}
}

// MintTabletToken — POST /store/v1/agent/tablet-tokens {tablet_id, tablet_name}
// (agent key) → 201 {id, token}. Minting again for the same tablet revokes its
// previous token. The agent hands the token to the tablet and never stores it.
func (h *DiagHandler) MintTabletToken(c *echo.Context) error {
	scope, _, ok := agentScope(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, errResp("device required"))
	}
	var req struct {
		TabletID   string `json:"tablet_id"`
		TabletName string `json:"tablet_name"`
	}
	if err := echo.BindBody(c, &req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	id, token, err := h.svc.Mint(c.Request().Context(), scope, req.TabletID, req.TabletName)
	if errors.Is(err, diag.ErrBadTablet) {
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	if err != nil {
		h.logger.Error().Ctx(c.Request().Context()).Err(err).Msg("mint tablet token failed")
		return c.JSON(http.StatusInternalServerError, errResp("mint failed"))
	}
	return c.JSON(http.StatusCreated, map[string]any{"id": id, "token": token})
}

// RevokeTabletToken — DELETE /store/v1/agent/tablet-tokens/:tablet_id (agent
// key) → 204, idempotent (the tablet was unpaired).
func (h *DiagHandler) RevokeTabletToken(c *echo.Context) error {
	scope, _, ok := agentScope(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, errResp("device required"))
	}
	if err := h.svc.Revoke(c.Request().Context(), scope, c.Param("tablet_id")); err != nil {
		h.logger.Error().Ctx(c.Request().Context()).Err(err).Msg("revoke tablet token failed")
		return c.JSON(http.StatusInternalServerError, errResp("revoke failed"))
	}
	return c.NoContent(http.StatusNoContent)
}

// PostReport — POST /store/v1/tablet/diag-reports (X-Quibase-Report-Key only)
// → 202 {id, stored:true}; a report already stored → 200 {id, duplicate:true};
// 400 malformed, 413 over 256 KB, 429 rate-limited (Retry-After).
func (h *DiagHandler) PostReport(c *echo.Context) error {
	ctx := c.Request().Context()
	tab := appMiddleware.TabletFromContext(ctx)
	if tab == nil {
		return c.JSON(http.StatusUnauthorized, errResp("report key required"))
	}
	if c.Request().ContentLength > diag.MaxBody {
		return c.JSON(http.StatusRequestEntityTooLarge, errResp(diag.ErrTooLarge.Error()))
	}
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, diag.MaxBody+1))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("could not read body"))
	}
	r, err := diag.ParseReport(body)
	switch {
	case errors.Is(err, diag.ErrTooLarge):
		return c.JSON(http.StatusRequestEntityTooLarge, errResp(err.Error()))
	case err != nil:
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	id, dup, err := h.svc.Store(ctx, tab.Scope, r, diag.ViaDirect, tab.TabletID, tab.TabletName)
	var rl *diag.RateLimitError
	switch {
	case errors.As(err, &rl):
		secs := diag.RetrySeconds(rl.RetryAfter)
		c.Response().Header().Set("Retry-After", strconv.Itoa(secs))
		return c.JSON(http.StatusTooManyRequests, map[string]any{"error": diag.ErrRateLimited.Error(), "code": "rate_limited", "retry_after": secs})
	case errors.Is(err, diag.ErrBadReport):
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	case err != nil:
		h.logger.Error().Ctx(ctx).Err(err).Msg("store diagnostics report failed")
		return c.JSON(http.StatusInternalServerError, errResp("store failed"))
	case dup:
		return c.JSON(http.StatusOK, map[string]any{"id": id, "stored": false, "duplicate": true})
	}
	return c.JSON(http.StatusAccepted, map[string]any{"id": id, "stored": true, "duplicate": false})
}

// AdminList — GET /admin/v1/diag-reports?tenant_id&kind&outcome&unresolved&limit
// → summaries, newest first.
func (h *DiagHandler) AdminList(c *echo.Context) error {
	f := diag.Filter{Kind: c.QueryParam("kind"), Outcome: c.QueryParam("outcome")}
	if v := c.QueryParam("tenant_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return c.JSON(http.StatusBadRequest, errResp("tenant_id must be a uuid"))
		}
		f.TenantID = &id
	}
	if v := c.QueryParam("unresolved"); v != "" {
		f.Unresolved, _ = strconv.ParseBool(v)
	}
	if v := c.QueryParam("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return c.JSON(http.StatusBadRequest, errResp("limit must be a positive integer"))
		}
		f.Limit = n
	}
	rows, err := h.svc.List(c.Request().Context(), f)
	if err != nil {
		h.logger.Error().Ctx(c.Request().Context()).Err(err).Msg("list diagnostics reports failed")
		return c.JSON(http.StatusInternalServerError, errResp("list failed"))
	}
	return c.JSON(http.StatusOK, rows)
}

// AdminGet — GET /admin/v1/diag-reports/:id (full uuid, or the 8-char prefix a
// page links with) → the summary + full report. 404 / 409 ambiguous prefix.
func (h *DiagHandler) AdminGet(c *echo.Context) error {
	d, err := h.svc.Get(c.Request().Context(), c.Param("id"))
	switch {
	case errors.Is(err, diag.ErrNotFound):
		return c.JSON(http.StatusNotFound, errResp("report not found"))
	case errors.Is(err, diag.ErrAmbiguous):
		return c.JSON(http.StatusConflict, errResp(err.Error()))
	case err != nil:
		h.logger.Error().Ctx(c.Request().Context()).Err(err).Msg("get diagnostics report failed")
		return c.JSON(http.StatusInternalServerError, errResp("get failed"))
	}
	return c.JSON(http.StatusOK, d)
}

// AdminResolve — PATCH /admin/v1/diag-reports/:id {resolved: bool} → the report.
func (h *DiagHandler) AdminResolve(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	var req struct {
		Resolved *bool `json:"resolved"`
	}
	if err := echo.BindBody(c, &req); err != nil || req.Resolved == nil {
		return c.JSON(http.StatusBadRequest, errResp("body must be {\"resolved\": true|false}"))
	}
	d, err := h.svc.SetResolved(c.Request().Context(), id, *req.Resolved)
	switch {
	case errors.Is(err, diag.ErrNotFound):
		return c.JSON(http.StatusNotFound, errResp("report not found"))
	case err != nil:
		h.logger.Error().Ctx(c.Request().Context()).Err(err).Msg("resolve diagnostics report failed")
		return c.JSON(http.StatusInternalServerError, errResp("update failed"))
	}
	return c.JSON(http.StatusOK, d)
}

// AdminListTokens — GET /admin/v1/tablet-tokens?location_id → report tokens
// (newest first; never the hash).
func (h *DiagHandler) AdminListTokens(c *echo.Context) error {
	var loc *uuid.UUID
	if v := c.QueryParam("location_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return c.JSON(http.StatusBadRequest, errResp("location_id must be a uuid"))
		}
		loc = &id
	}
	rows, err := h.svc.ListTokens(c.Request().Context(), loc)
	if err != nil {
		h.logger.Error().Ctx(c.Request().Context()).Err(err).Msg("list tablet tokens failed")
		return c.JSON(http.StatusInternalServerError, errResp("list failed"))
	}
	return c.JSON(http.StatusOK, rows)
}

// AdminRevokeToken — POST /admin/v1/tablet-tokens/:id/revoke → 204 (404 unknown).
func (h *DiagHandler) AdminRevokeToken(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	err = h.svc.RevokeTokenByID(c.Request().Context(), id)
	switch {
	case errors.Is(err, diag.ErrNotFound):
		return c.JSON(http.StatusNotFound, errResp("token not found"))
	case err != nil:
		h.logger.Error().Ctx(c.Request().Context()).Err(err).Msg("revoke tablet token failed")
		return c.JSON(http.StatusInternalServerError, errResp("revoke failed"))
	}
	return c.NoContent(http.StatusNoContent)
}
