package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	menuviewApp "quiccpos/main/internal/app/menuview"
	"quiccpos/main/internal/domain/menuview"

	"github.com/labstack/echo/v5"
)

// maxLayout caps a counter layout (SoftTouch's whole Food Menu, ~1,000 menu
// buttons + ~1,800 modifier buttons + ~850 flows, is well under 2 MB).
const maxLayout = 8 << 20

func (h *MenuViewHandler) layoutErr(c *echo.Context, err error) error {
	var le *menuviewApp.LayoutError
	switch {
	case errors.As(err, &le):
		return c.JSON(http.StatusUnprocessableEntity, map[string]any{"error": "the layout has problems", "code": "invalid_layout",
			"problems": le.Problems})
	case errors.Is(err, menuviewApp.ErrNotCounter):
		return c.JSON(http.StatusConflict, errCode(err.Error(), "not_counter"))
	case errors.Is(err, menuviewApp.ErrNoLayout):
		return c.JSON(http.StatusNotFound, errCode(err.Error(), "no_layout"))
	}
	return h.vErr(c, err)
}

// GET /store/v1/me/menu/views/:id/layout — a counter view's layout (by ids).
func (h *MenuViewHandler) MeGetLayout(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	l, err := h.svc.Layout(c.Request().Context(), lid, id)
	if err != nil {
		return h.layoutErr(c, err)
	}
	return c.JSON(http.StatusOK, l)
}

// PUT /store/v1/me/menu/views/:id/layout — replace a counter view's layout.
// 422 invalid_layout lists every problem (nothing is saved); unknown fields
// are a 400.
func (h *MenuViewHandler) MePutLayout(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, maxLayout+1))
	if err != nil {
		return badBody(c)
	}
	if len(body) > maxLayout {
		return c.JSON(http.StatusRequestEntityTooLarge, errCode("layout too large (max 8 MB)", "too_large"))
	}
	var l menuview.Layout
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return c.JSON(http.StatusBadRequest, errCode("layout: "+err.Error(), "bad_request"))
	}
	saved, err := h.svc.SetLayout(c.Request().Context(), lid, id, &l)
	if err != nil {
		return h.layoutErr(c, err)
	}
	return c.JSON(http.StatusOK, saved)
}

// DELETE /store/v1/me/menu/views/:id/layout — clear a counter view's layout.
func (h *MenuViewHandler) MeDeleteLayout(c *echo.Context) error {
	_, lid, _ := meScope(c)
	id, ok := idParam(c, "id")
	if !ok {
		return badID(c)
	}
	if err := h.svc.ClearLayout(c.Request().Context(), lid, id); err != nil {
		return h.layoutErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}
