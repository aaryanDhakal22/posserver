package handler

import (
	"math"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v5"

	"quiccpos/main/internal/shared/ratelimit"
)

// loginLimiter throttles failed password logins (manager session, operator
// and call-center logins) with the shared ratelimit.LoginGuard, the way
// agentsync throttles the Pi's link: a few failures per username, a ceiling
// per client address (c.RealIP(), the real caller since M3) and a high
// global safety valve that never stops a username and address with no
// failures of their own (H2). A manager password unlocks
// /store/v1/agent/link, which hands a Pi the store's secrets, so it must not
// be guessable here either.
type loginLimiter struct{ g *ratelimit.LoginGuard }

func newLoginLimiter() *loginLimiter { return &loginLimiter{g: ratelimit.NewLoginGuard()} }

// throttled answers 429 (with Retry-After) when the username or the caller's
// address is over its limit; it returns false when the attempt may go ahead.
func (l *loginLimiter) throttled(c *echo.Context, username string) (bool, error) {
	return l.throttledBody(c, username, errResp("too many failed logins — try again in a few minutes"))
}

// throttledCode is throttled with a {error, code} body (the channel API's shape).
func (l *loginLimiter) throttledCode(c *echo.Context, username string) (bool, error) {
	return l.throttledBody(c, username, errCode("too many failed sign-ins — try again in a few minutes", "too_many_attempts"))
}

func (l *loginLimiter) throttledBody(c *echo.Context, username string, body map[string]string) (bool, error) {
	ok, wait := l.g.Check(username, c.RealIP())
	if ok {
		return false, nil
	}
	c.Response().Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	return true, c.JSON(http.StatusTooManyRequests, body)
}

// failedFrom records a failed login for username from the request's caller.
func (l *loginLimiter) failedFrom(c *echo.Context, username string) {
	l.g.Fail(username, c.RealIP())
}

// failed records a failed login without the caller's address (the username
// and the valve count; the per-address ceiling doesn't). Prefer failedFrom.
func (l *loginLimiter) failed(username string) { l.g.Fail(username, "") }

func (l *loginLimiter) succeeded(username string) { l.g.Success(username) }
