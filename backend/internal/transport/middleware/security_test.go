package middleware

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

const testIndex = `<!doctype html><html><head>
<script>
  (function () { document.documentElement.classList.toggle('dark', true); })();
</script>
<link rel="modulepreload" href="/app/_app/immutable/entry/start.js">
</head><body><div style="display: contents">
<script>{ __sveltekit_x = { base: "/app" }; import("/app/_app/immutable/entry/start.js"); }</script>
<script src="/app/_app/immutable/entry/other.js"></script>
</div></body></html>`

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

func TestAppCSP(t *testing.T) {
	csp := AppCSP([]byte(testIndex), false, false)
	for _, want := range []string{
		"default-src 'self'", "object-src 'none'", "frame-ancestors 'none'", "base-uri 'self'", "form-action 'self'",
		"connect-src 'self';", "frame-src 'self' https:;",
		hashOf("\n  (function () { document.documentElement.classList.toggle('dark', true); })();\n"),
		hashOf(`{ __sveltekit_x = { base: "/app" }; import("/app/_app/immutable/entry/start.js"); }`),
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q:\n%s", want, csp)
		}
	}
	script := csp[strings.Index(csp, "script-src"):]
	script = script[:strings.Index(script, ";")]
	if strings.Contains(script, "unsafe-inline") || strings.Contains(script, "unsafe-eval") || strings.Contains(script, "*") || strings.Contains(script, "https:") {
		t.Errorf("script-src is too wide: %s", script)
	}
	if n := strings.Count(script, "'sha256-"); n != 2 {
		t.Errorf("script-src has %d hashes, want 2 (the two inline scripts; never the src one): %s", n, script)
	}
	if dev := AppCSP([]byte(testIndex), true, true); !strings.Contains(dev, "frame-src 'self' https: http:") || !strings.Contains(dev, "ws:") {
		t.Errorf("dev CSP must let the localhost storefront preview and Vite through: %s", dev)
	}
	// Staging: the storefront preview is plain http on the LAN, but nothing of Vite's.
	if stg := AppCSP([]byte(testIndex), false, true); !strings.Contains(stg, "frame-src 'self' https: http:") || strings.Contains(stg, "ws:") || !strings.Contains(stg, "connect-src 'self';") {
		t.Errorf("staging CSP = %s", stg)
	}
}

func TestSecurityHeaders(t *testing.T) {
	mw := SecurityHeaders(SecurityConfig{AppCSP: "app-policy", HSTSMaxAge: 31536000})
	run := func(path string, h map[string]string) http.Header {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		if err := mw(func(c *echo.Context) error { return c.String(http.StatusOK, "ok") })(echo.New().NewContext(req, rec)); err != nil {
			t.Fatal(err)
		}
		return rec.Header()
	}

	api := run("/store/v1/me/location", nil)
	if api.Get("X-Content-Type-Options") != "nosniff" || api.Get("X-Frame-Options") != "DENY" || api.Get("Referrer-Policy") == "" {
		t.Errorf("API headers = %v", api)
	}
	if csp := api.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("API CSP = %q", csp)
	}
	if api.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS on a plain-http request (the LAN, dev): a browser would pin https for a host that has none")
	}
	for _, p := range []string{"/app", "/app/store/payments", "/app/_app/immutable/x.js"} {
		if got := run(p, nil); got.Get("Content-Security-Policy") != "app-policy" || got.Get("X-Frame-Options") != "DENY" || got.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s headers = %v", p, got)
		}
	}
	if got := run("/apple", nil).Get("Content-Security-Policy"); got == "app-policy" {
		t.Error("/apple is not the app")
	}
	if got := run("/app/store", map[string]string{"X-Forwarded-Proto": "https"}).Get("Strict-Transport-Security"); got != "max-age=31536000" {
		t.Errorf("HSTS behind a TLS proxy = %q", got)
	}
	if got := run("/__fake/square/web-payments.js", nil); got.Get("X-Frame-Options") != "" || got.Get("Content-Security-Policy") != "" {
		t.Errorf("the dev fake Square must stay loadable across origins: %v", got)
	}
	// HSTS off.
	off := SecurityHeaders(SecurityConfig{})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	_ = off(func(c *echo.Context) error { return c.NoContent(204) })(echo.New().NewContext(req, rec))
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS sent with max-age 0")
	}
}

func TestCrossSiteGuard(t *testing.T) {
	const cookie, login = "quicc_store_session", "/store/v1/session"
	run := func(dev bool, method, path, host string, withCookie bool, h map[string]string) (int, bool) {
		req := httptest.NewRequest(method, path, nil)
		req.Host = host
		if withCookie {
			req.AddCookie(&http.Cookie{Name: cookie, Value: "x.y"})
		}
		for k, v := range h {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		ran := false
		if err := CrossSiteGuard(cookie, login, dev)(func(c *echo.Context) error {
			ran = true
			return c.NoContent(http.StatusNoContent)
		})(echo.New().NewContext(req, rec)); err != nil {
			t.Fatal(err)
		}
		return rec.Code, ran
	}
	const host = "main.quiccpos.com"
	cases := []struct {
		name   string
		dev    bool
		method string
		path   string
		cookie bool
		h      map[string]string
		pass   bool
	}{
		{"the SPA's own write", false, "POST", "/store/v1/me/printers", true, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://" + host}, true},
		{"another site's form post", false, "POST", "/store/v1/me/printers", true, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, false},
		{"another site, DELETE", false, "DELETE", "/store/v1/me/printers/1", true, map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		{"a store's website on a sibling subdomain", false, "PATCH", "/store/v1/me/location", true, map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "https://lombardis.quiccpos.com"}, false},
		{"same-site and the same host (http → https upgrade)", false, "POST", "/store/v1/me/printers", true, map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "http://" + host}, true},
		{"same-site without an Origin", false, "POST", "/store/v1/me/printers", true, map[string]string{"Sec-Fetch-Site": "same-site"}, false},
		{"older browser: Origin names us", false, "POST", "/store/v1/me/printers", true, map[string]string{"Origin": "https://" + host}, true},
		{"older browser: Origin is someone else", false, "POST", "/store/v1/me/printers", true, map[string]string{"Origin": "https://evil.example"}, false},
		{"a sandboxed frame (Origin: null)", false, "POST", "/store/v1/me/printers", true, map[string]string{"Origin": "null"}, false},
		{"behind a proxy that rewrote Host", false, "POST", "/store/v1/me/printers", true, map[string]string{"Origin": "https://shop.example", "X-Forwarded-Host": "shop.example"}, true},
		{"curl / the console / tests: no browser headers", false, "POST", "/store/v1/me/printers", true, nil, true},
		{"reads are never refused", false, "GET", "/store/v1/me/location", true, map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
		{"no cookie: a device or admin key call", false, "POST", "/store/v1/agent/rpc", false, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, true},
		{"the login itself, cross-site (login CSRF)", false, "POST", login, false, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, false},
		{"the login from the SPA", false, "POST", login, false, map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
		{"logout, cross-site", false, "DELETE", login, true, map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		{"dev: Vite on another port, plain http on the LAN", true, "POST", "/store/v1/me/printers", true, map[string]string{"Origin": "http://main.quiccpos.com:5173"}, true},
		{"prod: another port is another origin", false, "POST", "/store/v1/me/printers", true, map[string]string{"Origin": "https://main.quiccpos.com:8443"}, false},
	}
	for _, c := range cases {
		code, ran := run(c.dev, c.method, c.path, host, c.cookie, c.h)
		if ran != c.pass || (!c.pass && code != http.StatusForbidden) {
			t.Errorf("%s: ran=%v code=%d, want pass=%v", c.name, ran, code, c.pass)
		}
	}
}
