package handler

import (
	"errors"
	"net/http"
	"strings"

	geoApp "quiccpos/main/internal/app/geo"
	tenancyApp "quiccpos/main/internal/app/tenancy"
	"quiccpos/main/internal/domain/tenancy"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// GeoHandler serves the store pin + published map endpoints (2.0.9) and the
// dev-only geo provider override the pytest suite points at its fake server.
type GeoHandler struct {
	svc    *tenancyApp.Service
	geo    *geoApp.Service
	cfgPub *StoreConfigPublisher
	appEnv string
	logger zerolog.Logger
}

func NewGeoHandler(svc *tenancyApp.Service, geo *geoApp.Service, cfgPub *StoreConfigPublisher, appEnv string, logger zerolog.Logger) *GeoHandler {
	return &GeoHandler{svc: svc, geo: geo, cfgPub: cfgPub, appEnv: appEnv, logger: logger.With().Str("module", "geo-handler").Logger()}
}

type pinReq struct {
	Lat *float64 `json:"lat"`
	Lng *float64 `json:"lng"`
}

func (h *GeoHandler) setPin(c *echo.Context, tenantID, locationID uuid.UUID) error {
	var req pinReq
	if err := echo.BindBody(c, &req); err != nil || req.Lat == nil || req.Lng == nil {
		return c.JSON(http.StatusBadRequest, errResp("body must be {lat, lng}"))
	}
	ctx := c.Request().Context()
	l, err := h.svc.SetLocationPin(ctx, locationID, *req.Lat, *req.Lng)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusNotFound, errResp("location not found"))
	}
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	if tenantID == uuid.Nil {
		tenantID = l.TenantID
	}
	// Pushes store_config + re-routes the open deliveries from the new spot.
	if h.geo != nil {
		h.geo.StoreMoved(ctx, tenantID, l.ID)
	} else {
		h.cfgPub.Publish(ctx, tenantID, l.ID)
	}
	h.logger.Info().Ctx(ctx).Str("location_id", l.ID.String()).Msg("store pin placed by hand")
	return c.JSON(http.StatusOK, toLocationJSON(*l))
}

// PUT /store/v1/me/location/pin {lat, lng} — the manager dragged the store
// pin to the door. geo_source becomes manual (geocoding never moves it).
func (h *GeoHandler) MeSetPin(c *echo.Context) error {
	tid, lid, _ := meScope(c)
	return h.setPin(c, tid, lid)
}

// PUT /admin/v1/locations/:id/pin {lat, lng}
func (h *GeoHandler) AdminSetPin(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	return h.setPin(c, uuid.Nil, id)
}

// PATCH /admin/v1/locations/:id/map {url, sha256, version, bbox} — publish the
// store's offline map file (the console's map-build calls it); {"url": ""}
// clears it. Pushes store_config so the Pi downloads the new version.
func (h *GeoHandler) AdminSetMap(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("id must be a uuid"))
	}
	var req struct {
		URL     string    `json:"url"`
		SHA256  string    `json:"sha256"`
		Version string    `json:"version"`
		BBox    []float64 `json:"bbox"`
	}
	if err := echo.BindBody(c, &req); err != nil {
		return c.JSON(http.StatusBadRequest, errResp("invalid body"))
	}
	ctx := c.Request().Context()
	l, err := h.svc.SetLocationMap(ctx, id, tenancy.LocationMap{
		URL: strings.TrimSpace(req.URL), SHA256: strings.TrimSpace(req.SHA256), Version: strings.TrimSpace(req.Version), BBox: req.BBox,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusNotFound, errResp("location not found"))
	}
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp(err.Error()))
	}
	h.cfgPub.Publish(ctx, l.TenantID, l.ID)
	h.logger.Info().Ctx(ctx).Str("location_id", l.ID.String()).Str("map_version", l.MapVersion).Msg("store map published")
	return c.JSON(http.StatusOK, toLocationJSON(*l))
}

// GET /admin/v1/geo — which providers are active.
func (h *GeoHandler) AdminStatus(c *echo.Context) error {
	p := h.geo.Providers()
	return c.JSON(http.StatusOK, map[string]any{"provider": p.Name(), "routing": p.Routing()})
}

// POST /admin/v1/geo/test-providers {census_url, nominatim_url, osrm_url} —
// DEV ONLY (404 elsewhere): point main's geocoders + router at a test's fake
// server until DELETE restores the env configuration. Process-wide.
func (h *GeoHandler) DevSetProviders(c *echo.Context) error {
	if h.appEnv != "dev" {
		return c.JSON(http.StatusNotFound, errResp("not found"))
	}
	var req struct {
		CensusURL    string `json:"census_url"`
		NominatimURL string `json:"nominatim_url"`
		OSRMURL      string `json:"osrm_url"`
		Contact      string `json:"contact"`
	}
	if err := echo.BindBody(c, &req); err != nil || req.CensusURL == "" || req.NominatimURL == "" {
		return c.JSON(http.StatusBadRequest, errResp("body must be {census_url, nominatim_url, osrm_url?}"))
	}
	h.geo.SetProviders(geoApp.Config{CensusURL: req.CensusURL, NominatimURL: req.NominatimURL, OSRMURL: req.OSRMURL, Contact: req.Contact})
	h.logger.Warn().Str("census", req.CensusURL).Str("osrm", req.OSRMURL).Msg("dev: geo providers overridden for a test")
	return h.AdminStatus(c)
}

// DELETE /admin/v1/geo/test-providers — DEV ONLY: restore the env providers.
func (h *GeoHandler) DevResetProviders(c *echo.Context) error {
	if h.appEnv != "dev" {
		return c.JSON(http.StatusNotFound, errResp("not found"))
	}
	h.geo.ResetProviders()
	return h.AdminStatus(c)
}
