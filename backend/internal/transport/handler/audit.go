package handler

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"

	"quiccpos/main/internal/infra/database/repositories"
)

// AuditHandler reads the audit log (baseline 2026-11-12, contract §2.5zg):
// the store's own rows for its manager, every store's for the admin. The log
// is append-only; nothing here writes.
type AuditHandler struct {
	repo   *repositories.AuditRepository
	logger zerolog.Logger
}

func NewAuditHandler(repo *repositories.AuditRepository, logger zerolog.Logger) *AuditHandler {
	return &AuditHandler{repo: repo, logger: logger.With().Str("handler", "audit").Logger()}
}

// auditFilter reads ?before=<id>&limit=1..200 (default 50)&entity=&entity_id=.
func auditFilter(c *echo.Context) (repositories.AuditFilter, bool) {
	f := repositories.AuditFilter{Limit: 50, Entity: c.QueryParam("entity"), EntityID: c.QueryParam("entity_id")}
	if v := c.QueryParam("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			return f, false
		}
		f.BeforeID = n
	}
	if v := c.QueryParam("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			return f, false
		}
		f.Limit = n
	}
	return f, true
}

func (h *AuditHandler) answer(c *echo.Context, f repositories.AuditFilter) error {
	rows, err := h.repo.List(c.Request().Context(), f)
	if err != nil {
		h.logger.Error().Err(err).Msg("audit list")
		return c.JSON(http.StatusInternalServerError, errCode("audit log unavailable", "internal"))
	}
	var next *int64
	if len(rows) == f.Limit {
		id := rows[len(rows)-1].ID
		next = &id
	}
	return c.JSON(http.StatusOK, map[string]any{"entries": rows, "next_before": next})
}

// GET /store/v1/me/reports/audit — the store's audit log, newest first.
func (h *AuditHandler) MeAudit(c *echo.Context) error {
	_, lid, _ := meScope(c)
	f, ok := auditFilter(c)
	if !ok {
		return c.JSON(http.StatusBadRequest, errCode("before: a row id; limit: 1-200", "bad_request"))
	}
	f.LocationID = &lid
	return h.answer(c, f)
}

// GET /admin/v1/audit — every store's audit log (?location_id= narrows).
func (h *AuditHandler) AdminAudit(c *echo.Context) error {
	f, ok := auditFilter(c)
	if !ok {
		return c.JSON(http.StatusBadRequest, errCode("before: a row id; limit: 1-200", "bad_request"))
	}
	if v := c.QueryParam("location_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return c.JSON(http.StatusBadRequest, errCode("location_id: a uuid", "bad_request"))
		}
		f.LocationID = &id
	}
	return h.answer(c, f)
}
