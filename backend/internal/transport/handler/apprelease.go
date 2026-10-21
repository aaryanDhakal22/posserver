package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"quiccpos/main/internal/infra/database/models"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// Tablet app (Quibase) releases: the console uploads a signed APK
// (admin), each agent polls for the newest and downloads it (agent device
// key), then serves it to its paired tablets over the LAN. main is just the
// shelf — it never pushes anything.

const (
	maxAppAPK    = 100 << 20
	keepReleases = 5
)

var appNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

type AppReleaseHandler struct {
	pool   *pgxpool.Pool
	logger zerolog.Logger
}

func NewAppReleaseHandler(pool *pgxpool.Pool, logger zerolog.Logger) *AppReleaseHandler {
	return &AppReleaseHandler{pool: pool, logger: logger.With().Str("module", "app-release-handler").Logger()}
}

// AppRelease is the public shape of a release (no bytes).
type AppRelease struct {
	App         string    `json:"app"`
	VersionCode int64     `json:"version_code"`
	VersionName string    `json:"version_name"`
	SHA256      string    `json:"sha256"`
	Size        int64     `json:"size"`
	CreatedAt   time.Time `json:"created_at"`
}

func appParam(c *echo.Context) (string, bool) {
	app := c.QueryParam("app")
	if app == "" {
		app = "quibase"
	}
	return app, appNameRe.MatchString(app)
}

// Upload — POST /admin/v1/app-releases?app=quibase&version_code=N&version_name=X
// body = the APK. Optional X-Sha256 is checked against the body.
func (h *AppReleaseHandler) Upload(c *echo.Context) error {
	app, ok := appParam(c)
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("app must be a lowercase name"))
	}
	code, err := strconv.ParseInt(c.QueryParam("version_code"), 10, 64)
	if err != nil || code <= 0 {
		return c.JSON(http.StatusBadRequest, errResp("version_code must be a positive integer"))
	}
	name := strings.TrimSpace(c.QueryParam("version_name"))
	if name == "" || len(name) > 64 {
		return c.JSON(http.StatusBadRequest, errResp("version_name is required (max 64)"))
	}
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, maxAppAPK+1))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("could not read the upload"))
	}
	if len(body) > maxAppAPK {
		return c.JSON(http.StatusRequestEntityTooLarge, errResp("APK too large (max 100 MB)"))
	}
	if len(body) < 4 || !bytes.Equal(body[:4], []byte("PK\x03\x04")) {
		return c.JSON(http.StatusBadRequest, errResp("body is not an APK (zip)"))
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	if want := strings.ToLower(strings.TrimSpace(c.Request().Header.Get("X-Sha256"))); want != "" && want != digest {
		return c.JSON(http.StatusBadRequest, errResp("sha256 mismatch (upload corrupted)"))
	}
	ctx := c.Request().Context()
	q := models.New(h.pool)
	latest, err := q.GetLatestAppRelease(ctx, app)
	if err == nil && code <= latest.VersionCode {
		return c.JSON(http.StatusConflict, map[string]any{
			"error": "version_code must be newer than the latest release", "code": "not_newer", "latest": latest.VersionCode,
		})
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusInternalServerError, errResp("lookup failed"))
	}
	row, err := q.CreateAppRelease(ctx, models.CreateAppReleaseParams{
		App: app, VersionCode: code, VersionName: name, Sha256: digest, Size: int64(len(body)), Apk: body,
	})
	if err != nil {
		h.logger.Error().Ctx(ctx).Err(err).Msg("store app release failed")
		return c.JSON(http.StatusInternalServerError, errResp("store failed"))
	}
	if err := q.PruneAppReleases(ctx, models.PruneAppReleasesParams{App: app, Limit: keepReleases}); err != nil {
		h.logger.Warn().Ctx(ctx).Err(err).Msg("prune old app releases failed")
	}
	h.logger.Info().Ctx(ctx).Str("app", app).Int64("version_code", code).Str("version_name", name).Int("bytes", len(body)).Msg("App release uploaded")
	return c.JSON(http.StatusCreated, AppRelease{App: row.App, VersionCode: row.VersionCode, VersionName: row.VersionName,
		SHA256: row.Sha256, Size: row.Size, CreatedAt: row.CreatedAt.Time})
}

// List — GET /admin/v1/app-releases?app=quibase (newest first).
func (h *AppReleaseHandler) List(c *echo.Context) error {
	app, ok := appParam(c)
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("bad app"))
	}
	rows, err := models.New(h.pool).ListAppReleases(c.Request().Context(), app)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("list failed"))
	}
	out := make([]AppRelease, 0, len(rows))
	for _, r := range rows {
		out = append(out, AppRelease{App: r.App, VersionCode: r.VersionCode, VersionName: r.VersionName,
			SHA256: r.Sha256, Size: r.Size, CreatedAt: r.CreatedAt.Time})
	}
	return c.JSON(http.StatusOK, out)
}

// Latest — GET /store/v1/agent/app/latest?app=quibase → the newest release,
// or 204 when there is none.
func (h *AppReleaseHandler) Latest(c *echo.Context) error {
	app, ok := appParam(c)
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("bad app"))
	}
	r, err := models.New(h.pool).GetLatestAppRelease(c.Request().Context(), app)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.NoContent(http.StatusNoContent)
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("lookup failed"))
	}
	return c.JSON(http.StatusOK, AppRelease{App: r.App, VersionCode: r.VersionCode, VersionName: r.VersionName,
		SHA256: r.Sha256, Size: r.Size, CreatedAt: r.CreatedAt.Time})
}

// APK — GET /store/v1/agent/app/releases/:code/apk?app=quibase → the bytes.
func (h *AppReleaseHandler) APK(c *echo.Context) error {
	app, ok := appParam(c)
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("bad app"))
	}
	code, err := strconv.ParseInt(c.Param("code"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("bad version code"))
	}
	apk, err := models.New(h.pool).GetAppReleaseAPK(c.Request().Context(), models.GetAppReleaseAPKParams{App: app, VersionCode: code})
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusNotFound, errResp("no such release"))
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("lookup failed"))
	}
	c.Response().Header().Set(echo.HeaderContentDisposition, `attachment; filename="`+app+`-`+strconv.FormatInt(code, 10)+`.apk"`)
	return c.Blob(http.StatusOK, "application/vnd.android.package-archive", apk)
}

// Delete — DELETE /admin/v1/app-releases/:code?app=quibase (pull a bad build;
// agents keep what they cached until a newer release appears).
func (h *AppReleaseHandler) Delete(c *echo.Context) error {
	app, ok := appParam(c)
	if !ok {
		return c.JSON(http.StatusBadRequest, errResp("bad app"))
	}
	code, err := strconv.ParseInt(c.Param("code"), 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errResp("bad version code"))
	}
	n, err := models.New(h.pool).DeleteAppRelease(c.Request().Context(), models.DeleteAppReleaseParams{App: app, VersionCode: code})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, errResp("delete failed"))
	}
	if n == 0 {
		return c.JSON(http.StatusNotFound, errResp("no such release"))
	}
	return c.NoContent(http.StatusNoContent)
}
