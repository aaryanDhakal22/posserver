package handler

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"

	"quiccpos/main/internal/shared/ratelimit"
)

// loginLimiter throttles failed password logins (manager session, operator
// login) the way agentsync throttles the Pi's link: a few failures per
// username per window, plus a global ceiling against spraying usernames.
// A manager password unlocks /store/v1/agent/link, which hands a Pi the
// store's secrets, so it must not be guessable here either.
type loginLimiter struct{ w *ratelimit.Window }

const (
	loginFailMax    = 5
	loginFailWindow = 10 * time.Minute
	loginGlobalKey  = "*"
	loginGlobalMax  = 30
)

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{w: ratelimit.New(loginFailMax, loginFailWindow)}
}

func loginKey(username string) string { return "u:" + strings.ToLower(strings.TrimSpace(username)) }

// throttled answers 429 (with Retry-After) when username, or everyone, is
// over the limit; it returns false when the attempt may go ahead.
func (l *loginLimiter) throttled(c *echo.Context, username string) (bool, error) {
	key := loginKey(username)
	if l.w.Allow(key) && l.w.AllowN(loginGlobalKey, loginGlobalMax) {
		return false, nil
	}
	wait := max(l.w.RetryAfter(key), l.w.RetryAfterN(loginGlobalKey, loginGlobalMax))
	c.Response().Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	return true, c.JSON(http.StatusTooManyRequests, errResp("too many failed logins — try again in a few minutes"))
}

func (l *loginLimiter) failed(username string) {
	l.w.Add(loginKey(username))
	l.w.Add(loginGlobalKey)
}

func (l *loginLimiter) succeeded(username string) { l.w.Reset(loginKey(username)) }
