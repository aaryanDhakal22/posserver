package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	familiesApp "quiccpos/main/internal/app/families"
	"quiccpos/main/internal/domain/menu"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// FamiliesHandler is Store › Menu › Families (/store/v1/me/menu/families*,
// session-scoped; migration 00045).
type FamiliesHandler struct {
	svc    *familiesApp.Service
	logger zerolog.Logger
}

func NewFamiliesHandler(svc *familiesApp.Service, lg zerolog.Logger) *FamiliesHandler {
	return &FamiliesHandler{svc: svc, logger: lg.With().Str("module", "families-handler").Logger()}
}

func (h *FamiliesHandler) fErr(c *echo.Context, err error) error {
	var inCoupon *familiesApp.InCoupon
	switch {
	case errors.As(err, &inCoupon):
		return c.JSON(http.StatusConflict, map[string]any{"error": "coupons name this family: merge it into another instead",
			"code": "family_in_coupon", "coupons": inCoupon.Coupons})
	case errors.Is(err, familiesApp.ErrNotFound):
		return c.JSON(http.StatusNotFound, errCode(err.Error(), "not_found"))
	case errors.Is(err, familiesApp.ErrNameTaken):
		return c.JSON(http.StatusConflict, errCode("another family of that kind has this name", "name_taken"))
	case errors.Is(err, familiesApp.ErrWrongKind):
		return c.JSON(http.StatusBadRequest, errCode(err.Error(), "wrong_kind"))
	case errors.Is(err, menu.ErrBadFamily), errors.Is(err, familiesApp.ErrNotStore), errors.Is(err, familiesApp.ErrTooMany):
		return c.JSON(http.StatusBadRequest, errCode(err.Error(), "bad_family"))
	}
	h.logger.Error().Err(err).Msg("families")
	return c.JSON(http.StatusInternalServerError, errCode("families failed", "internal"))
}

// GET /store/v1/me/menu/families
func (h *FamiliesHandler) MeList(c *echo.Context) error {
	_, lid, _ := meScope(c)
	out, err := h.svc.List(c.Request().Context(), lid)
	if err != nil {
		return h.fErr(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

// POST /store/v1/me/menu/families {kind, name, class?}
func (h *FamiliesHandler) MeCreate(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	var req struct {
		Kind  string `json:"kind"`
		Name  string `json:"name"`
		Class string `json:"class"`
	}
	if !decodeStrict(c, &req) {
		return badBody(c)
	}
	f, err := h.svc.Create(c.Request().Context(), tid, lid, menu.Family{Kind: req.Kind, Name: req.Name, Class: req.Class})
	if err != nil {
		return h.fErr(c, err)
	}
	return c.JSON(http.StatusCreated, f)
}

// PATCH /store/v1/me/menu/families/:id {name?, class?}
func (h *FamiliesHandler) MeUpdate(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	var req struct {
		Name  *string `json:"name"`
		Class *string `json:"class"`
	}
	if !decodeStrict(c, &req) {
		return badBody(c)
	}
	f, err := h.svc.Update(c.Request().Context(), lid, id, req.Name, req.Class)
	if err != nil {
		return h.fErr(c, err)
	}
	return c.JSON(http.StatusOK, f)
}

// DELETE /store/v1/me/menu/families/:id
func (h *FamiliesHandler) MeDelete(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	if err := h.svc.Delete(c.Request().Context(), lid, id); err != nil {
		return h.fErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// POST /store/v1/me/menu/families/:id/merge {into}
func (h *FamiliesHandler) MeMerge(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	var req struct {
		Into uuid.UUID `json:"into"`
	}
	if !decodeStrict(c, &req) || req.Into == uuid.Nil {
		return badBody(c)
	}
	out, err := h.svc.Merge(c.Request().Context(), lid, id, req.Into)
	if err != nil {
		return h.fErr(c, err)
	}
	return c.JSON(http.StatusOK, out)
}

// POST /store/v1/me/menu/families/assign {family_id|null, items?, options?}
func (h *FamiliesHandler) MeAssign(c *echo.Context) error {
	_, lid, _ := meScope(c)
	var req struct {
		FamilyID *uuid.UUID  `json:"family_id"`
		Items    []uuid.UUID `json:"items"`
		Options  []uuid.UUID `json:"options"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 512<<10)) // 2,000 ids
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil {
		return badBody(c)
	}
	ni, no, err := h.svc.Assign(c.Request().Context(), lid, req.FamilyID, req.Items, req.Options)
	if err != nil {
		return h.fErr(c, err)
	}
	return c.JSON(http.StatusOK, map[string]int{"items": ni, "options": no})
}
