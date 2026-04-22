package handler

import (
	"net/http"

	authSvc "quiccpos/main/internal/app/auth"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

type AuthHandler struct {
	svc           *authSvc.Service
	adminPasscode string
	logger        zerolog.Logger
}

func NewAuthHandler(svc *authSvc.Service, adminPasscode string, logger zerolog.Logger) *AuthHandler {
	return &AuthHandler{
		svc:           svc,
		adminPasscode: adminPasscode,
		logger:        logger.With().Str("module", "auth-handler").Logger(),
	}
}

// POST /api/v1/auth/key
func (h *AuthHandler) SetKey(c *echo.Context) error {
	log := zerolog.Ctx(c.Request().Context()).With().Str("module", "auth-handler").Logger()

	if h.adminPasscode == "" {
		log.Warn().Msg("SetKey rejected: ADMIN_PASSCODE not configured on server")
		return c.JSON(http.StatusServiceUnavailable, errResp("admin passcode not configured"))
	}

	passcode := c.Request().Header.Get("X-Admin-Passcode")
	if passcode != h.adminPasscode {
		log.Warn().Msg("SetKey rejected: invalid admin passcode")
		return c.JSON(http.StatusForbidden, errResp("invalid admin passcode"))
	}

	var req struct {
		Key string `json:"key"`
	}
	if err := c.Bind(&req); err != nil || req.Key == "" {
		log.Warn().Msg("SetKey rejected: missing or invalid key in body")
		return c.JSON(http.StatusBadRequest, errResp("key is required"))
	}

	if err := h.svc.SetKey(c.Request().Context(), req.Key); err != nil {
		log.Error().Err(err).Msg("SetKey failed to persist key")
		return c.JSON(http.StatusInternalServerError, errResp("failed to set key"))
	}

	log.Info().Msg("API key updated")
	return c.JSON(http.StatusOK, map[string]string{"key": req.Key})
}
