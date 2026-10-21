package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestLoginLimiter(t *testing.T) {
	e := echo.New()
	l := newLoginLimiter()
	try := func(user string) (bool, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		stop, _ := l.throttled(e.NewContext(httptest.NewRequest(http.MethodPost, "/", nil), rec), user)
		return stop, rec
	}

	for i := 0; i < loginFailMax; i++ {
		if stop, _ := try("Lombardispizza"); stop {
			t.Fatalf("attempt %d throttled, want allowed", i+1)
		}
		l.failed(" lombardispizza ")
	}
	stop, rec := try("lombardispizza")
	if !stop || rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("after %d failures: stop=%v code=%d retry=%q, want 429 with Retry-After", loginFailMax, stop, rec.Code, rec.Header().Get("Retry-After"))
	}
	if stop, _ := try("someoneelse"); stop {
		t.Fatal("another username is throttled by one user's failures")
	}

	// A success clears the user's count.
	l2 := newLoginLimiter()
	for i := 0; i < loginFailMax-1; i++ {
		l2.failed("u")
	}
	l2.succeeded("u")
	l2.failed("u")
	rec = httptest.NewRecorder()
	if stop, _ := l2.throttled(e.NewContext(httptest.NewRequest(http.MethodPost, "/", nil), rec), "u"); stop {
		t.Fatal("a successful login should reset the user's failures")
	}

	// The global ceiling stops username spraying.
	l3 := newLoginLimiter()
	for i := 0; i < loginGlobalMax; i++ {
		l3.failed(string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}
	rec = httptest.NewRecorder()
	if stop, _ := l3.throttled(e.NewContext(httptest.NewRequest(http.MethodPost, "/", nil), rec), "fresh"); !stop {
		t.Fatal("global ceiling not applied")
	}
}
