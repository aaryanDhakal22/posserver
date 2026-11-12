package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

func TestAppAPKCap(t *testing.T) {
	for in, want := range map[string]int64{
		"":      256 << 20, // the default
		"100":   100 << 20,
		" 300 ": 300 << 20,
		"0":     256 << 20,
		"-5":    256 << 20,
		"2000":  256 << 20, // over 1024: not believed
		"big":   256 << 20,
	} {
		if got := AppAPKCap(func(string) string { return in }); got != want {
			t.Errorf("APP_RELEASE_MAX_MB=%q: %d, want %d", in, got, want)
		}
	}
}

// The cap is the handler's setting: an upload over it is 413 naming the cap,
// one under it gets past the size check (here: stopped as "not an APK", before
// any database call).
func TestAppReleaseUploadCap(t *testing.T) {
	t.Setenv("APP_RELEASE_MAX_MB", "1")
	h := NewAppReleaseHandler(nil, zerolog.Nop())
	upload := func(n int) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/v1/app-releases?app=terminal&version_code=7&version_name=0.1.0",
			bytes.NewReader(bytes.Repeat([]byte("x"), n)))
		rec := httptest.NewRecorder()
		if err := h.Upload(echo.New().NewContext(req, rec)); err != nil {
			t.Fatal(err)
		}
		return rec
	}
	if rec := upload(1<<20 + 1); rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "max 1 MB") {
		t.Fatalf("over the cap: %d %s", rec.Code, rec.Body.String())
	}
	if rec := upload(1 << 20); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not an APK") {
		t.Fatalf("at the cap: %d %s", rec.Code, rec.Body.String())
	}

	t.Setenv("APP_RELEASE_MAX_MB", "")
	if got := NewAppReleaseHandler(nil, zerolog.Nop()).maxAPK; got != 256<<20 {
		t.Fatalf("default cap %d", got)
	}
}
