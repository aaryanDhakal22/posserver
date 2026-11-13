package handler

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"quiccpos/main/internal/domain/settingsmap"
)

// SettingsMap is GET /store/v1/settings-map (open, contract §2.5zm): main's
// settings map, so the apps link a person straight to a page and field of
// the store's main (base URL + path + "#" + anchor) in main's own words.
func SettingsMap(c *echo.Context) error {
	c.Response().Header().Set("Cache-Control", "public, max-age=300")
	return c.Blob(http.StatusOK, "application/json", settingsmap.Raw)
}
