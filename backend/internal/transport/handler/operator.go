package handler

import (
	"net/http"
	"strings"

	menuApp "quiccpos/main/internal/app/menu"
	operatorApp "quiccpos/main/internal/app/operator"
	"quiccpos/main/internal/observability"
	appMiddleware "quiccpos/main/internal/transport/middleware"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// OperatorHandler serves both the operator read surface (login/me/menu, operator
// bearer auth) and the admin Operators tab (/admin/v1/operators/*, admin auth).
type OperatorHandler struct {
	svc     *operatorApp.Service
	menuSvc *menuApp.Service
	meters  observability.Meters
	logins  *loginLimiter
	logger  zerolog.Logger
}

func NewOperatorHandler(svc *operatorApp.Service, menuSvc *menuApp.Service, meters observability.Meters, logger zerolog.Logger) *OperatorHandler {
	return &OperatorHandler{svc: svc, menuSvc: menuSvc, meters: meters, logins: newLoginLimiter(), logger: logger.With().Str("module", "operator-handler").Logger()}
}

// --- read surface (operator bearer token) ----------------------------------

// POST /store/v1/operator/login (unauthenticated) → mint a read-only token.
func (h *OperatorHandler) Login(c *echo.Context) error {
	ctx := c.Request().Context()
	var req struct{ Username, Password string }
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	if stop, err := h.logins.throttled(c, req.Username); stop {
		return err
	}
	op, err := h.svc.Authenticate(ctx, req.Username, req.Password)
	if err != nil {
		if operatorApp.IsInvalidCredentials(err) {
			h.logins.failed(req.Username)
			return c.JSON(http.StatusUnauthorized, errResp("invalid username or password"))
		}
		return c.JSON(http.StatusInternalServerError, errResp("login failed"))
	}
	h.logins.succeeded(req.Username)
	token, err := h.svc.MintToken(ctx, op.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("failed to mint token"))
	}
	h.meters.OperatorTokensMinted.Add(ctx, 1)
	allowed, _ := h.svc.ListAllowedLocations(ctx, op.ID)
	return c.JSON(http.StatusOK, map[string]any{
		"token":             token,
		"operator":          op,
		"allowed_locations": allowed,
	})
}

// GET /store/v1/operator/me (operator) — whoami.
func (h *OperatorHandler) Me(c *echo.Context) error {
	oc, ok := appMiddleware.OperatorFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, errResp("login required"))
	}
	return c.JSON(http.StatusOK, map[string]any{
		"operator_id":       oc.OperatorID.String(),
		"username":          oc.Username,
		"allowed_locations": oc.Allowed,
	})
}

// GET /store/v1/operator/menu?location_id= (operator) — full menu, if allowed.
func (h *OperatorHandler) Menu(c *echo.Context) error {
	ctx := c.Request().Context()
	lid, err := uuid.Parse(c.QueryParam("location_id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("location_id must be a uuid"))
	}
	if !appMiddleware.OperatorAllowsLocation(ctx, lid) {
		return c.JSON(http.StatusForbidden, errResp("not allowed for this location"))
	}
	full, err := h.menuSvc.GetFullMenu(ctx, lid)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("failed to load menu"))
	}
	return c.JSON(http.StatusOK, full)
}

// --- admin CRUD (ADMIN_API_KEY) --------------------------------------------

type operatorAdminJSON struct {
	ID          string   `json:"id"`
	Username    string   `json:"username"`
	DisplayName string   `json:"display_name"`
	LocationIDs []string `json:"location_ids"`
	CreatedAt   string   `json:"created_at"`
}

// GET /admin/v1/operators
func (h *OperatorHandler) AdminList(c *echo.Context) error {
	ctx := c.Request().Context()
	ops, err := h.svc.List(ctx)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("list failed"))
	}
	out := make([]operatorAdminJSON, 0, len(ops))
	for _, op := range ops {
		locs, _ := h.svc.ListAllowedLocations(ctx, op.ID)
		ids := make([]string, 0, len(locs))
		for _, l := range locs {
			ids = append(ids, l.LocationID.String())
		}
		out = append(out, operatorAdminJSON{ID: op.ID.String(), Username: op.Username, DisplayName: op.DisplayName, LocationIDs: ids, CreatedAt: op.CreatedAt.Format("2006-01-02T15:04:05Z07:00")})
	}
	return c.JSON(http.StatusOK, out)
}

// POST /admin/v1/operators
func (h *OperatorHandler) AdminCreate(c *echo.Context) error {
	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	op, err := h.svc.Create(c.Request().Context(), req.Username, req.Password, req.DisplayName)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	return c.JSON(http.StatusCreated, op)
}

// PATCH /admin/v1/operators/:id — edit display name and/or rotate password.
func (h *OperatorHandler) AdminUpdate(c *echo.Context) error {
	ctx := c.Request().Context()
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	var req struct {
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	op, err := h.svc.UpdateProfile(ctx, id, req.DisplayName)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	if req.Password != "" {
		if err := h.svc.SetPassword(ctx, id, req.Password); err != nil {
			return c.JSON(http.StatusBadRequest, errResp(err.Error()))
		}
	}
	return c.JSON(http.StatusOK, op)
}

// DELETE /admin/v1/operators/:id
func (h *OperatorHandler) AdminDelete(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	if err := h.svc.Delete(c.Request().Context(), id); err != nil {
		return c.JSON(http.StatusInternalServerError, errResp(err.Error()))
	}
	return c.NoContent(http.StatusNoContent)
}

// PUT /admin/v1/operators/:id/locations — {location_ids: [uuid...]}
func (h *OperatorHandler) AdminSetLocations(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	var req struct {
		LocationIDs []string `json:"location_ids"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	lids := make([]uuid.UUID, 0, len(req.LocationIDs))
	for _, s := range req.LocationIDs {
		u, err := uuid.Parse(s)
		if err != nil {
			return c.JSON(http.StatusBadRequest, errResp("location_ids must be uuids"))
		}
		lids = append(lids, u)
	}
	if err := h.svc.SetAllowedLocations(c.Request().Context(), id, lids); err != nil {
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	return c.NoContent(http.StatusNoContent)
}

// POST /admin/v1/operators/:id/tokens/revoke — kill all of an operator's tokens.
func (h *OperatorHandler) AdminRevokeTokens(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	if err := h.svc.RevokeTokens(c.Request().Context(), id); err != nil {
		return c.JSON(http.StatusInternalServerError, errResp(err.Error()))
	}
	h.meters.OperatorTokensRevoked.Add(c.Request().Context(), 1)
	return c.NoContent(http.StatusNoContent)
}

// --- /channel/v1/operator (main 3.0 step 7) -----------------------------------

// POST /channel/v1/operator/login {username, password} → a 12-hour sign-in
// for the channel API: {token, expires_at, operator, locations}. Then every
// /channel/v1 call carries the token as Bearer + the store as X-Location-ID.
func (h *OperatorHandler) ChannelLogin(c *echo.Context) error {
	ctx := c.Request().Context()
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := echo.BindBody(c, &req); err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid body", "bad_request"))
	}
	if stop, err := h.logins.throttledCode(c, req.Username); stop {
		return err
	}
	op, err := h.svc.Authenticate(ctx, req.Username, req.Password)
	if err != nil {
		if operatorApp.IsInvalidCredentials(err) {
			h.logins.failed(req.Username)
			return c.JSON(http.StatusUnauthorized, errCode("invalid username or password", "invalid_credentials"))
		}
		return c.JSON(http.StatusInternalServerError, errCode("sign-in failed", "internal"))
	}
	h.logins.succeeded(req.Username)
	token, exp, err := h.svc.MintSession(ctx, op.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errCode("sign-in failed", "internal"))
	}
	h.meters.OperatorTokensMinted.Add(ctx, 1)
	allowed, err := h.svc.ListAllowedLocations(ctx, op.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errCode("sign-in failed", "internal"))
	}
	locs := make([]map[string]any, 0, len(allowed))
	for _, a := range allowed {
		name := ""
		if l, err := h.svc.Location(ctx, a.LocationID); err == nil && l != nil {
			name = l.Name
		}
		locs = append(locs, map[string]any{"location_id": a.LocationID, "tenant_id": a.TenantID, "name": name})
	}
	return c.JSON(http.StatusOK, map[string]any{
		"token": token, "expires_at": exp,
		"operator":  map[string]any{"id": op.ID, "username": op.Username, "display_name": op.DisplayName},
		"locations": locs,
	})
}

// POST /channel/v1/operator/logout (Bearer the sign-in) → 204, also for one
// already signed out.
func (h *OperatorHandler) ChannelLogout(c *echo.Context) error {
	raw := strings.TrimSpace(strings.TrimPrefix(c.Request().Header.Get("Authorization"), "Bearer "))
	if err := h.svc.RevokeSession(c.Request().Context(), raw); err != nil {
		if operatorApp.IsInvalidToken(err) {
			return c.JSON(http.StatusUnauthorized, errCode("not an operator sign-in", "unauthorized"))
		}
		return c.JSON(http.StatusInternalServerError, errCode("sign-out failed", "internal"))
	}
	return c.NoContent(http.StatusNoContent)
}
