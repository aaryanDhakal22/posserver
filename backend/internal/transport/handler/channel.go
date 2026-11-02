package handler

import (
	"errors"
	"net/http"

	channelApp "quiccpos/main/internal/app/channel"
	tenancyApp "quiccpos/main/internal/app/tenancy"
	channelDom "quiccpos/main/internal/domain/channel"
	"quiccpos/main/internal/transport/channelapi"
	appMiddleware "quiccpos/main/internal/transport/middleware"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// ChannelHandler serves the admin's channel management (/admin/v1/channels,
// admin key) and the channel API's identity + spec (/channel/v1, channel key).
type ChannelHandler struct {
	svc     *channelApp.Service
	tenancy *tenancyApp.Service
	logger  zerolog.Logger
}

func NewChannelHandler(svc *channelApp.Service, tenancy *tenancyApp.Service, logger zerolog.Logger) *ChannelHandler {
	return &ChannelHandler{svc: svc, tenancy: tenancy, logger: logger.With().Str("module", "channel-handler").Logger()}
}

// --- /channel/v1 ------------------------------------------------------------

// GET /channel/v1/me (channel) — the channel behind the key and its store.
func (h *ChannelHandler) Me(c *echo.Context) error {
	ch, ok := appMiddleware.ChannelFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, errCode("missing channel key", "unauthorized"))
	}
	loc, err := h.tenancy.GetLocation(c.Request().Context(), ch.LocationID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errCode("store lookup failed", "internal"))
	}
	return c.JSON(http.StatusOK, map[string]any{
		"channel": map[string]any{"id": ch.ID, "kind": ch.Kind, "name": ch.Name, "permissions": ch.Permissions},
		"store":   map[string]any{"tenant_id": ch.TenantID, "location_id": ch.LocationID, "name": loc.Name, "timezone": loc.Timezone},
	})
}

// GET /channel/v1/openapi.json (none) — the contract adapters build against.
func (h *ChannelHandler) Spec(c *echo.Context) error {
	return c.Blob(http.StatusOK, "application/json", channelapi.Spec)
}

// --- /admin/v1/channels -----------------------------------------------------

// channelAdminJSON is a channel as the admin UI sees it (+ key once on
// create / rotate).
type channelAdminJSON struct {
	channelDom.Channel
	Active bool   `json:"active"`
	Key    string `json:"key,omitempty"`
}

func adminChannel(c *channelDom.Channel, key string) channelAdminJSON {
	return channelAdminJSON{Channel: *c, Active: c.Active(), Key: key}
}

// GET /admin/v1/channels[?location_id=]
func (h *ChannelHandler) AdminList(c *echo.Context) error {
	var loc *uuid.UUID
	if v := c.QueryParam("location_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return c.JSON(http.StatusBadRequest, errResp("location_id must be a uuid"))
		}
		loc = &id
	}
	list, err := h.svc.List(c.Request().Context(), loc)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("list failed"))
	}
	out := make([]channelAdminJSON, 0, len(list))
	for i := range list {
		out = append(out, adminChannel(&list[i], ""))
	}
	return c.JSON(http.StatusOK, out)
}

// POST /admin/v1/channels {location_id, kind, name, permissions?} → the
// channel + its key (shown once).
func (h *ChannelHandler) AdminCreate(c *echo.Context) error {
	var req struct {
		LocationID  string                  `json:"location_id"`
		Kind        string                  `json:"kind"`
		Name        string                  `json:"name"`
		Permissions *channelDom.Permissions `json:"permissions"`
	}
	if err := echo.BindBody(c, &req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	lid, err := uuid.Parse(req.LocationID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("location_id must be a uuid"))
	}
	ch, key, err := h.svc.Create(c.Request().Context(), lid, channelDom.Kind(req.Kind), req.Name, req.Permissions)
	if err != nil {
		return h.adminErr(c, err)
	}
	return c.JSON(http.StatusCreated, adminChannel(ch, key))
}

// GET /admin/v1/channels/:id
func (h *ChannelHandler) AdminGet(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	ch, err := h.svc.Get(c.Request().Context(), id)
	if err != nil {
		return h.adminErr(c, err)
	}
	return c.JSON(http.StatusOK, adminChannel(ch, ""))
}

// PATCH /admin/v1/channels/:id {name?, permissions?}
func (h *ChannelHandler) AdminUpdate(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	var req struct {
		Name        *string                 `json:"name"`
		Permissions *channelDom.Permissions `json:"permissions"`
	}
	if err := echo.BindBody(c, &req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	ch, err := h.svc.Update(c.Request().Context(), id, req.Name, req.Permissions)
	if err != nil {
		return h.adminErr(c, err)
	}
	return c.JSON(http.StatusOK, adminChannel(ch, ""))
}

// POST /admin/v1/channels/:id/rotate → a new key (shown once); the old one
// stops working, a revoked channel is active again.
func (h *ChannelHandler) AdminRotate(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	ch, key, err := h.svc.Rotate(c.Request().Context(), id)
	if err != nil {
		return h.adminErr(c, err)
	}
	return c.JSON(http.StatusOK, adminChannel(ch, key))
}

// POST /admin/v1/channels/:id/revoke
func (h *ChannelHandler) AdminRevoke(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	ch, err := h.svc.Revoke(c.Request().Context(), id)
	if err != nil {
		return h.adminErr(c, err)
	}
	return c.JSON(http.StatusOK, adminChannel(ch, ""))
}

// DELETE /admin/v1/channels/:id
func (h *ChannelHandler) AdminDelete(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	if err := h.svc.Delete(c.Request().Context(), id); err != nil {
		return h.adminErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *ChannelHandler) adminErr(c *echo.Context, err error) error {
	switch {
	case errors.Is(err, channelApp.ErrNotFound), errors.Is(err, channelApp.ErrUnknownPlace):
		return c.JSON(http.StatusNotFound, errResp(err.Error()))
	case errors.Is(err, channelApp.ErrBuiltin):
		return c.JSON(http.StatusConflict, errResp(err.Error()))
	case errors.Is(err, channelApp.ErrBadKind), errors.Is(err, channelApp.ErrNameRequired), errors.Is(err, channelApp.ErrBadPayment):
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	h.logger.Error().Err(err).Msg("channel admin")
	return c.JSON(http.StatusInternalServerError, errResp("channel update failed"))
}
