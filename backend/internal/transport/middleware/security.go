package middleware

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
)

// Security headers and the cross-site guard (security review, hardening).
// main sets these itself so they hold whatever sits in front of it; a
// Traefik headers middleware may repeat them (deploy notes in contract §6).

// SecurityConfig is what SecurityHeaders needs to know about the deployment.
type SecurityConfig struct {
	// AppCSP is the Content-Security-Policy of the SPA's documents and
	// assets (/app…): AppCSP() from the embedded index.html.
	AppCSP string
	// HSTSMaxAge (seconds) is sent as Strict-Transport-Security on requests
	// that arrived over TLS (directly, or X-Forwarded-Proto: https from the
	// proxy). 0 = never.
	HSTSMaxAge int
}

// apiCSP: an API answer is data, never a page — nothing may load from it and
// nobody may frame it.
const apiCSP = "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

var (
	inlineScript = regexp.MustCompile(`(?is)<script(\s[^>]*)?>(.*?)</script>`)
	scriptSrc    = regexp.MustCompile(`(?i)\ssrc\s*=`)
)

// AppCSP builds the SPA's policy from its index.html: scripts only from main
// itself plus the page's own inline scripts by hash (the theme snippet and
// SvelteKit's start-up script, whatever the build made them), styles from
// main plus inline (Svelte writes style attributes), images from main, data:
// / blob: (the image cropper) and https (map tiles, a store's website
// images), frames only for the Website page's preview of the storefront
// (https; plain http too outside prod),
// and nobody may frame the app (clickjacking). API calls go to main only.
//
// dev = APP_ENV dev (Vite's server and socket are allowed); httpFrames =
// not prod (dev and staging preview a storefront served over plain http on
// localhost / the LAN; prod's is https).
func AppCSP(indexHTML []byte, dev, httpFrames bool) string {
	scripts := []string{"'self'"}
	for _, m := range inlineScript.FindAllSubmatch(indexHTML, -1) {
		if scriptSrc.Match(m[1]) || len(m[2]) == 0 {
			continue
		}
		sum := sha256.Sum256(m[2])
		scripts = append(scripts, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	frames := "frame-src 'self' https:"
	connect := "connect-src 'self'"
	if httpFrames {
		frames += " http:"
	}
	if dev {
		connect += " ws: http://localhost:* http://127.0.0.1:*" // Vite's dev server and HMR socket
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src " + strings.Join(scripts, " "),
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob: https:",
		"font-src 'self' data:",
		connect,
		frames,
		"worker-src 'self' blob:",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}

// IsHTTPS reports whether the request reached us over TLS: directly, or
// through a TLS-terminating proxy that says so.
func IsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// SecurityHeaders sets, on every response: X-Content-Type-Options: nosniff,
// X-Frame-Options: DENY, Referrer-Policy, a Content-Security-Policy (the
// SPA's for /app, the data-only one elsewhere) and, over TLS,
// Strict-Transport-Security. The dev-only fake Square (/__fake/…) is left
// alone: the storefront loads its script and card form across origins.
func SecurityHeaders(cfg SecurityConfig) echo.MiddlewareFunc {
	hsts := ""
	if cfg.HSTSMaxAge > 0 {
		hsts = "max-age=" + strconv.Itoa(cfg.HSTSMaxAge)
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			p := c.Request().URL.Path
			if strings.HasPrefix(p, "/__fake/") {
				return next(c)
			}
			h := c.Response().Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			if (p == "/app" || strings.HasPrefix(p, "/app/")) && cfg.AppCSP != "" {
				h.Set("Content-Security-Policy", cfg.AppCSP)
			} else {
				h.Set("Content-Security-Policy", apiCSP)
			}
			if hsts != "" && IsHTTPS(c.Request()) {
				h.Set("Strict-Transport-Security", hsts)
			}
			return next(c)
		}
	}
}

// CrossSiteGuard refuses (403, code cross_site) a state-changing request
// that rides on the store-manager session cookie, or is the session login
// itself, when the browser says it comes from another site. The cookie is
// SameSite=Lax already; this is the second lock, and it also covers a page
// on a sibling subdomain (a store's website), which SameSite lets through.
//
//   - Sec-Fetch-Site: same-origin / none passes, cross-site is refused,
//     same-site is decided by Origin;
//   - no Sec-Fetch-Site (an older browser, or plain http on the LAN): an
//     Origin header must name the host the request was sent to;
//   - neither header: not a browser page (curl, the console, tests) — passes.
//
// Requests without the cookie (device keys, the admin key, channel keys) are
// not its business: a cross-site page can't supply those.
func CrossSiteGuard(cookieName, loginPath string, dev bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			r := c.Request()
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				return next(c)
			}
			if _, err := r.Cookie(cookieName); err != nil && r.URL.Path != loginPath {
				return next(c)
			}
			if crossSite(r, dev) {
				return c.JSON(http.StatusForbidden, map[string]string{"error": "cross-site request refused", "code": "cross_site"})
			}
			return next(c)
		}
	}
}

func crossSite(r *http.Request, dev bool) bool {
	site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
	switch site {
	case "same-origin", "none":
		return false
	case "cross-site":
		return true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return site == "same-site" // a browser always names the origin of a same-site write
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return true // "null" (a sandboxed frame, a file) or junk
	}
	for _, host := range []string{r.Host, firstField(r.Header.Get("X-Forwarded-Host"))} {
		if host == "" {
			continue
		}
		if strings.EqualFold(u.Host, host) {
			return false
		}
		// Dev: the Vite server proxies the SPA's calls to main on another port.
		if dev && strings.EqualFold(u.Hostname(), hostOnly(host)) {
			return false
		}
	}
	return true
}

func firstField(v string) string {
	f, _, _ := strings.Cut(v, ",")
	return strings.TrimSpace(f)
}

func hostOnly(hostport string) string {
	if u, err := url.Parse("//" + hostport); err == nil {
		return u.Hostname()
	}
	return hostport
}
