package transport

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/labstack/echo/v5"
)

func appFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                    {Data: []byte("<!doctype html><body>app</body>")},
		"_app/immutable/entry/app.js":   {Data: []byte("export const x=1")},
		"favicon.png":                   {Data: []byte("\x89PNG")},
	}
}

func TestServeApp_NotBuilt(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/app", nil)
	rec := httptest.NewRecorder()
	if err := serveApp(e.NewContext(req, rec), appFS(), false); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("not-built: want 503, got %d", rec.Code)
	}
}

func TestServeApp_Index(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/app", nil)
	rec := httptest.NewRecorder()
	if err := serveApp(e.NewContext(req, rec), appFS(), true); err != nil {
		t.Fatalf("err: %v", err)
	}
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("index: got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestServeApp_HashedAssetImmutable(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/app/_app/immutable/entry/app.js", nil)
	rec := httptest.NewRecorder()
	if err := serveApp(e.NewContext(req, rec), appFS(), true); err != nil {
		t.Fatalf("err: %v", err)
	}
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("asset: got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("asset cache: %q", rec.Header().Get("Cache-Control"))
	}
}

func TestServeApp_SPAFallback(t *testing.T) {
	// A client route (no file, extensionless) falls back to index.html.
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/app/store/printers", nil)
	rec := httptest.NewRecorder()
	if err := serveApp(e.NewContext(req, rec), appFS(), true); err != nil {
		t.Fatalf("err: %v", err)
	}
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("fallback: got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestServeApp_MissingAssetIs404(t *testing.T) {
	// A missing path WITH an extension is a real 404 (not a fallback).
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/app/_app/immutable/missing.js", nil)
	rec := httptest.NewRecorder()
	err := serveApp(e.NewContext(req, rec), appFS(), true)
	if !errors.Is(err, echo.ErrNotFound) {
		t.Fatalf("missing asset: want ErrNotFound, got %v (code %d)", err, rec.Code)
	}
}

func TestAppContentType(t *testing.T) {
	cases := map[string]string{
		"a.js": "text/javascript; charset=utf-8",
		"a.css": "text/css; charset=utf-8",
		"a.woff2": "font/woff2",
		"a.svg": "image/svg+xml",
		"a.webmanifest": "application/manifest+json",
	}
	for name, want := range cases {
		if got := appContentType(name); got != want {
			t.Errorf("appContentType(%q)=%q want %q", name, got, want)
		}
	}
}
