package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	siteApp "quiccpos/main/internal/app/site"
	"quiccpos/main/internal/domain/site"
	appMiddleware "quiccpos/main/internal/transport/middleware"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// SiteHandler serves store websites (weborder stage 2): the storefront's
// platform keys (admin), each store's hosts (admin + store manager) and
// GET /channel/v1/site.
type SiteHandler struct {
	svc    *siteApp.Service
	logger zerolog.Logger
}

func NewSiteHandler(svc *siteApp.Service, logger zerolog.Logger) *SiteHandler {
	return &SiteHandler{svc: svc, logger: logger.With().Str("module", "site-handler").Logger()}
}

func (h *SiteHandler) sErr(c *echo.Context, err error) error {
	switch {
	case errors.Is(err, siteApp.ErrNotFound):
		return c.JSON(http.StatusNotFound, errCode(err.Error(), "not_found"))
	case errors.Is(err, siteApp.ErrUnknownPlace):
		return c.JSON(http.StatusNotFound, errCode(err.Error(), "not_found"))
	case errors.Is(err, siteApp.ErrNameRequired), errors.Is(err, site.ErrBadHost):
		return c.JSON(http.StatusBadRequest, errCode(err.Error(), "bad_request"))
	case errors.Is(err, siteApp.ErrPlatformHost):
		return c.JSON(http.StatusForbidden, errCode(err.Error(), "admin_only"))
	case errors.Is(err, site.ErrHostTaken):
		return c.JSON(http.StatusConflict, errCode(err.Error(), "host_taken"))
	case errors.Is(err, site.ErrBadLook):
		return c.JSON(http.StatusBadRequest, errCode(err.Error(), "bad_settings"))
	case errors.Is(err, site.ErrBadImage):
		return c.JSON(http.StatusBadRequest, errCode(err.Error(), "bad_image"))
	case errors.Is(err, siteApp.ErrNothingToPublish):
		return c.JSON(http.StatusConflict, errCode(err.Error(), "nothing_to_publish"))
	case errors.Is(err, siteApp.ErrBadPreview):
		return c.JSON(http.StatusForbidden, errCode(err.Error(), "bad_preview"))
	case errors.Is(err, siteApp.ErrImageNotFound):
		return c.JSON(http.StatusNotFound, errCode(err.Error(), "not_found"))
	}
	h.logger.Error().Err(err).Msg("site")
	return c.JSON(http.StatusInternalServerError, errCode("site request failed", "internal"))
}

// --- /admin/v1/platform-keys ---------------------------------------------------------

// GET /admin/v1/platform-keys
func (h *SiteHandler) AdminListKeys(c *echo.Context) error {
	keys, err := h.svc.ListKeys(c.Request().Context())
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"keys": keys})
}

// POST /admin/v1/platform-keys {name} → the key, once.
func (h *SiteHandler) AdminCreateKey(c *echo.Context) error {
	var body struct {
		Name string `json:"name"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid JSON body", "bad_request"))
	}
	k, raw, err := h.svc.CreateKey(c.Request().Context(), body.Name)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]any{"key": raw, "platform_key": k})
}

// POST /admin/v1/platform-keys/:id/revoke
func (h *SiteHandler) AdminRevokeKey(c *echo.Context) error {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid id", "bad_request"))
	}
	k, err := h.svc.RevokeKey(c.Request().Context(), id)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, k)
}

// --- hosts -------------------------------------------------------------------------

type domainBody struct {
	Host    string `json:"host"`
	Primary bool   `json:"primary"`
}

func (h *SiteHandler) domains(c *echo.Context, lid uuid.UUID) error {
	ds, err := h.svc.Domains(c.Request().Context(), lid)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"domains": ds, "base_domain": h.svc.BaseDomain()})
}

// GET /admin/v1/locations/:id/site-domains
func (h *SiteHandler) AdminDomains(c *echo.Context) error {
	lid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid location id", "bad_request"))
	}
	return h.domains(c, lid)
}

// POST /admin/v1/locations/:id/site-domains {host, primary} — any host,
// platform subdomains verified at once.
func (h *SiteHandler) AdminAddDomain(c *echo.Context) error {
	lid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid location id", "bad_request"))
	}
	var body domainBody
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid JSON body", "bad_request"))
	}
	d, err := h.svc.AddDomain(c.Request().Context(), lid, body.Host, body.Primary, true)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusCreated, d)
}

// PATCH /admin/v1/site-domains/:host {verified}
func (h *SiteHandler) AdminVerifyDomain(c *echo.Context) error {
	var body struct {
		Verified *bool `json:"verified"`
	}
	if err := c.Bind(&body); err != nil || body.Verified == nil {
		return c.JSON(http.StatusBadRequest, errCode("send {verified: true|false}", "bad_request"))
	}
	d, err := h.svc.SetVerified(c.Request().Context(), c.Param("host"), *body.Verified)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, d)
}

// GET /store/v1/me/site/domains
func (h *SiteHandler) MeDomains(c *echo.Context) error {
	_, lid, _ := meScope(c)
	return h.domains(c, lid)
}

// POST /store/v1/me/site/domains {host, primary} — the store's own domain
// (unverified until checked); platform subdomains are admin-only.
func (h *SiteHandler) MeAddDomain(c *echo.Context) error {
	_, lid, _ := meScope(c)
	var body domainBody
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, errCode("invalid JSON body", "bad_request"))
	}
	d, err := h.svc.AddDomain(c.Request().Context(), lid, body.Host, body.Primary, false)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusCreated, d)
}

// POST /store/v1/me/site/domains/:host/primary
func (h *SiteHandler) MeSetPrimary(c *echo.Context) error {
	_, lid, _ := meScope(c)
	if err := h.svc.SetPrimary(c.Request().Context(), lid, c.Param("host")); err != nil {
		return h.sErr(c, err)
	}
	return h.domains(c, lid)
}

// DELETE /store/v1/me/site/domains/:host
func (h *SiteHandler) MeRemoveDomain(c *echo.Context) error {
	_, lid, _ := meScope(c)
	if err := h.svc.RemoveDomain(c.Request().Context(), lid, c.Param("host")); err != nil {
		return h.sErr(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// --- /channel/v1/site -------------------------------------------------------------

// GET /channel/v1/site — the store's published look + texts, its hosts and
// version. ETag = a hash of the answer; a matching If-None-Match is a 304.
func (h *SiteHandler) ChannelSite(c *echo.Context) error {
	ch, ok := appMiddleware.ChannelFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, errCode("missing channel key", "unauthorized"))
	}
	if !ch.Permissions.Menu {
		return c.JSON(http.StatusForbidden, errCode("this channel may not read the site", "forbidden"))
	}
	v, err := h.svc.SiteFor(c.Request().Context(), ch.LocationID, c.QueryParam("preview"))
	if err != nil {
		return h.sErr(c, err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return h.sErr(c, err)
	}
	sum := sha256.Sum256(raw)
	etag := `"s` + hex.EncodeToString(sum[:8]) + `"`
	c.Response().Header().Set("ETag", etag)
	c.Response().Header().Set("Cache-Control", "no-cache")
	if c.Request().Header.Get("If-None-Match") == etag {
		return c.NoContent(http.StatusNotModified)
	}
	return c.Blob(http.StatusOK, "application/json", raw)
}

// --- the Website page (/store/v1/me/site/website + images) ---------------------------
// (the settings document: domain/site/look.go; OpenAPI schema SiteSettings)

// GET /store/v1/me/site/website — the draft (or the published look), what it
// changes, and the private preview link.
func (h *SiteHandler) MeWebsite(c *echo.Context) error {
	_, lid, _ := meScope(c)
	w, err := h.svc.Website(c.Request().Context(), lid)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, w)
}

// PUT /store/v1/me/site/website/draft — the whole document (checked; unknown
// fields refused).
func (h *SiteHandler) MeSaveDraft(c *echo.Context) error {
	_, lid, _ := meScope(c)
	raw, err := io.ReadAll(io.LimitReader(c.Request().Body, site.MaxLookBytes+1))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errCode("could not read the body", "bad_request"))
	}
	w, err := h.svc.SaveDraft(c.Request().Context(), lid, raw)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, w)
}

// DELETE /store/v1/me/site/website/draft
func (h *SiteHandler) MeDiscardDraft(c *echo.Context) error {
	_, lid, _ := meScope(c)
	w, err := h.svc.DiscardDraft(c.Request().Context(), lid)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, w)
}

// POST /store/v1/me/site/website/publish — 409 nothing_to_publish without a draft.
func (h *SiteHandler) MePublish(c *echo.Context) error {
	_, lid, _ := meScope(c)
	by := "manager"
	if appMiddleware.StoreImpersonatorFromContext(c.Request().Context()) != "" {
		by = "admin"
	}
	w, err := h.svc.Publish(c.Request().Context(), lid, by)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusOK, w)
}

// POST /store/v1/me/site/images?kind=logo|cover — the image as the raw body
// (PNG, JPEG, WebP; a logo may be SVG), ≤ 2 MB → 201 {id, kind, content_type, size}.
func (h *SiteHandler) MeAddImage(c *echo.Context) error {
	_, lid, _ := meScope(c)
	data, err := io.ReadAll(io.LimitReader(c.Request().Body, site.MaxImageBytes+1))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errCode("could not read the image", "bad_request"))
	}
	img, err := h.svc.AddImage(c.Request().Context(), lid, c.QueryParam("kind"), data)
	if err != nil {
		return h.sErr(c, err)
	}
	return c.JSON(http.StatusCreated, img)
}

// GET /store/v1/me/site/images/:id
func (h *SiteHandler) MeImage(c *echo.Context) error {
	_, lid, _ := meScope(c)
	return h.image(c, lid)
}

// GET /channel/v1/site/images/:id — a logo or cover photo of the channel's store.
func (h *SiteHandler) ChannelImage(c *echo.Context) error {
	ch, ok := appMiddleware.ChannelFromContext(c.Request().Context())
	if !ok {
		return c.JSON(http.StatusUnauthorized, errCode("missing channel key", "unauthorized"))
	}
	if !ch.Permissions.Menu {
		return c.JSON(http.StatusForbidden, errCode("this channel may not read the site", "forbidden"))
	}
	return h.image(c, ch.LocationID)
}

// image answers with the bytes. An id never changes, so it caches for a year;
// an SVG can't run anything (sandboxed, no scripts).
func (h *SiteHandler) image(c *echo.Context, lid uuid.UUID) error {
	img, err := h.svc.Image(c.Request().Context(), lid, c.Param("id"))
	if err != nil {
		return h.sErr(c, err)
	}
	hd := c.Response().Header()
	hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	return c.Blob(http.StatusOK, img.ContentType, img.Bytes)
}

// GET /store/v1/agent/receipt-logo (agent key) — the store's published logo
// for the top of the Pi's receipts: the bytes with their Content-Type and
// ETag (store_config's receipt_logo.etag; a matching If-None-Match is a 304);
// 404 no_logo when the store has none or it is an SVG.
func (h *SiteHandler) AgentReceiptLogo(c *echo.Context) error {
	scope, _, ok := agentScope(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, errResp("device required"))
	}
	img, err := h.svc.ReceiptLogo(c.Request().Context(), scope.LocationID)
	if err != nil {
		return h.sErr(c, err)
	}
	if img == nil {
		return c.JSON(http.StatusNotFound, errCode("the store has no printable logo", "no_logo"))
	}
	etag := siteApp.LogoETag(img.Bytes)
	hd := c.Response().Header()
	hd.Set("ETag", etag)
	hd.Set("Cache-Control", "no-cache")
	hd.Set("X-Content-Type-Options", "nosniff")
	if etagMatches(c.Request().Header.Get("If-None-Match"), etag) {
		return c.NoContent(http.StatusNotModified)
	}
	return c.Blob(http.StatusOK, img.ContentType, img.Bytes)
}

// etagMatches reads If-None-Match leniently: a list, weak tags, unquoted.
func etagMatches(header, etag string) bool {
	want := strings.Trim(etag, `"`)
	for _, v := range strings.Split(header, ",") {
		v = strings.TrimSpace(v)
		if v == "*" {
			return true
		}
		if strings.Trim(strings.TrimPrefix(v, "W/"), `"`) == want && want != "" {
			return true
		}
	}
	return false
}
