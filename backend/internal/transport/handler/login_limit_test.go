package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"quiccpos/main/internal/shared/ratelimit"
	"quiccpos/main/internal/transport/clientip"
)

// loginCtx is a login request as main sees it behind Traefik: the peer is
// the proxy, the caller is in X-Forwarded-For.
func loginCtx(t *testing.T, e *echo.Echo, client string) (*echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/store/v1/session", nil)
	req.RemoteAddr = "172.18.0.5:40000"
	if client != "" {
		req.Header.Set("X-Forwarded-For", client)
	}
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func proxiedEcho(t *testing.T) *echo.Echo {
	t.Helper()
	e := echo.New()
	x, err := clientip.FromEnv("", "")
	if err != nil {
		t.Fatal(err)
	}
	e.IPExtractor = x
	return e
}

func TestLoginLimiter(t *testing.T) {
	t.Setenv("LOGIN_IP_MAX", "")
	t.Setenv("LOGIN_GLOBAL_MAX", "")
	e := proxiedEcho(t)
	l := newLoginLimiter()
	try := func(user, client string) (bool, *httptest.ResponseRecorder) {
		c, rec := loginCtx(t, e, client)
		stop, _ := l.throttled(c, user)
		return stop, rec
	}
	fail := func(user, client string) {
		c, _ := loginCtx(t, e, client)
		l.failedFrom(c, user)
	}

	for i := 0; i < ratelimit.LoginUserMax; i++ {
		if stop, _ := try("Lombardispizza", "198.51.100.1"); stop {
			t.Fatalf("attempt %d throttled, want allowed", i+1)
		}
		fail(" lombardispizza ", "198.51.100.1")
	}
	stop, rec := try("lombardispizza", "198.51.100.1")
	if !stop || rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("after %d failures: stop=%v code=%d retry=%q, want 429 with Retry-After", ratelimit.LoginUserMax, stop, rec.Code, rec.Header().Get("Retry-After"))
	}
	if stop, _ := try("someoneelse", "198.51.100.1"); stop {
		t.Fatal("another username is throttled by one user's failures")
	}

	// A success clears the user's count.
	l.succeeded("lombardispizza")
	if stop, _ := try("lombardispizza", "198.51.100.1"); stop {
		t.Fatal("a successful login should reset the user's failures")
	}
}

// H2, as it was found: one anonymous caller fails 30 logins on made-up
// usernames and every manager gets 429 before their password is looked at.
func TestLoginLimiterOneCallerCannotLockEveryoneOut(t *testing.T) {
	t.Setenv("LOGIN_IP_MAX", "")
	t.Setenv("LOGIN_GLOBAL_MAX", "")
	e := proxiedEcho(t)
	l := newLoginLimiter()
	const attacker, manager = "203.0.113.66", "198.51.100.20"
	blockedAt := 0
	for i := 0; i < 200; i++ {
		c, _ := loginCtx(t, e, attacker)
		user := fmt.Sprintf("nobody%d", i)
		if stop, _ := l.throttled(c, user); stop {
			blockedAt = i
			break
		}
		l.failedFrom(c, user)
	}
	if blockedAt != ratelimit.LoginIPMax {
		t.Fatalf("the attacker's address was stopped after %d failures, want %d", blockedAt, ratelimit.LoginIPMax)
	}
	c, rec := loginCtx(t, e, manager)
	if stop, _ := l.throttled(c, "lombardispizza"); stop {
		t.Fatalf("a manager is locked out by another caller's failures (HTTP %d)", rec.Code)
	}
	// The attacker can't dodge its ceiling by naming addresses: Traefik
	// appends the one it saw, and that one counts.
	req := httptest.NewRequest(http.MethodPost, "/store/v1/session", nil)
	req.RemoteAddr = "172.18.0.5:40000"
	req.Header.Set("X-Forwarded-For", "8.8.8.8, "+attacker)
	if stop, _ := l.throttled(e.NewContext(req, httptest.NewRecorder()), "another"); !stop {
		t.Fatal("a spoofed X-Forwarded-For entry got the attacker past its ceiling")
	}
}

// Without the caller's address (failed, kept for callers that have no
// request at hand) the username still locks.
func TestLoginLimiterFailedWithoutAddress(t *testing.T) {
	e := proxiedEcho(t)
	l := newLoginLimiter()
	for i := 0; i < ratelimit.LoginUserMax; i++ {
		l.failed("u")
	}
	c, _ := loginCtx(t, e, "198.51.100.1")
	if stop, _ := l.throttled(c, "u"); !stop {
		t.Fatal("username not throttled")
	}
}
