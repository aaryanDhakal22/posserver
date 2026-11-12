package transport

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	appMiddleware "quiccpos/main/internal/transport/middleware"
	"quiccpos/main/internal/transport/webapp"
)

// The route list maps the whole surface: anonymous callers get it on dev
// only. Elsewhere it is a 404 like any unknown path, unless the admin key
// comes with the request.
func TestRouteListDevOnly(t *testing.T) {
	infos := []RouteInfo{{"GET", "/health", "none", "Liveness"}}
	call := func(env string, h map[string]string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/__routes", nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		err := routeListHandler(env, "the-admin-key", "1.2.3", infos)(echo.New().NewContext(req, rec))
		if errors.Is(err, echo.ErrNotFound) {
			return http.StatusNotFound, ""
		}
		if err != nil {
			t.Fatal(err)
		}
		return rec.Code, rec.Body.String()
	}
	if code, body := call("dev", nil); code != http.StatusOK || !strings.Contains(body, "/health") {
		t.Fatalf("dev: %d %s", code, body)
	}
	for _, env := range []string{"prod", "staging"} {
		if code, body := call(env, nil); code != http.StatusNotFound || body != "" {
			t.Fatalf("%s, anonymous: %d %q, want 404", env, code, body)
		}
		if code, _ := call(env, map[string]string{"Authorization": "Bearer wrong"}); code != http.StatusNotFound {
			t.Fatalf("%s, wrong key: %d, want 404", env, code)
		}
		if code, body := call(env, map[string]string{"Authorization": "Bearer the-admin-key"}); code != http.StatusOK || !strings.Contains(body, "/health") {
			t.Fatalf("%s, admin key: %d %s", env, code, body)
		}
		if code, _ := call(env, map[string]string{"X-Admin-Key": "the-admin-key"}); code != http.StatusOK {
			t.Fatalf("%s, X-Admin-Key: %d", env, code)
		}
	}
}

// AddSecurityMiddlewares on a real Echo: the headers are on every answer,
// errors and unknown paths included, and a cross-site write on the manager's
// cookie stops before the handler.
func TestSecurityMiddlewaresInstalled(t *testing.T) {
	e := echo.New()
	AddSecurityMiddlewares(e, "prod", 600)
	wrote := false
	e.POST("/store/v1/me/printers", func(c *echo.Context) error { wrote = true; return c.NoContent(http.StatusCreated) })
	e.GET("/app", func(c *echo.Context) error { return c.HTML(http.StatusOK, "<html></html>") })

	do := func(method, path string, h map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	for _, path := range []string{"/app", "/nope", "/store/v1/me/printers"} {
		h := do(http.MethodGet, path, map[string]string{"X-Forwarded-Proto": "https"}).Header()
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" ||
			!strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") || h.Get("Strict-Transport-Security") != "max-age=600" {
			t.Errorf("GET %s headers = %v", path, h)
		}
	}
	if csp := do(http.MethodGet, "/app", nil).Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "http:") {
		t.Errorf("the app's CSP on prod = %q", csp)
	}
	cookie := "quicc_store_session=x.y"
	if rec := do(http.MethodPost, "/store/v1/me/printers", map[string]string{"Cookie": cookie, "Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}); rec.Code != http.StatusForbidden || wrote {
		t.Fatalf("cross-site write: %d, handler ran=%v", rec.Code, wrote)
	}
	if rec := do(http.MethodPost, "/store/v1/me/printers", map[string]string{"Cookie": cookie, "Sec-Fetch-Site": "same-origin"}); rec.Code != http.StatusCreated || !wrote {
		t.Fatalf("the SPA's write: %d, handler ran=%v", rec.Code, wrote)
	}
}

// The policy built from the embedded UI (when a real build is embedded)
// allows each of its inline scripts by hash and nothing looser.
func TestAppCSPFromEmbeddedBuild(t *testing.T) {
	if !webapp.Built() {
		t.Skip("no UI build embedded (task ui-build)")
	}
	index, _, ok := readAppFile(webapp.FS(), "index.html")
	if !ok {
		t.Fatal("index.html unreadable")
	}
	csp := appMiddleware.AppCSP(index, false)
	inline := strings.Count(string(index), "<script>") // SvelteKit and our theme snippet write bare <script>
	if got := strings.Count(csp, "'sha256-"); got != inline || inline == 0 {
		t.Fatalf("CSP has %d script hashes for %d inline scripts:\n%s", got, inline, csp)
	}
	if strings.Contains(csp[strings.Index(csp, "script-src"):strings.Index(csp, "style-src")], "unsafe") {
		t.Fatalf("script-src allows unsafe sources: %s", csp)
	}
}
