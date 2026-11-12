package ratelimit

import (
	"strings"
	"time"
)

// LoginGuard throttles FAILED password logins (manager session, operator and
// call-center logins, the Pi's link). Three counters share one 10-minute
// window:
//
//   - per username (LoginUserMax): guessing one store's password;
//   - per client address (LOGIN_IP_MAX, default LoginIPMax): one caller
//     spraying usernames;
//   - everyone together (LOGIN_GLOBAL_MAX, default LoginGlobalMax): a safety
//     valve against a spray from many addresses.
//
// Security review H2: the global counter used to be a low ceiling (30)
// checked before the password, so one anonymous caller could keep every
// manager out. Now the valve never stops a login whose username and address
// have no failures of their own: other people's failures can't lock a
// manager out, and while the valve is shut a sprayer gets one guess per
// username instead of LoginUserMax.
type LoginGuard struct {
	w         *Window
	ipMax     int
	globalMax int
}

const (
	LoginUserMax   = 5
	LoginIPMax     = 20
	LoginGlobalMax = 600
	LoginWindow    = 10 * time.Minute

	loginGlobalKey = "*"
)

// NewLoginGuard reads LOGIN_IP_MAX / LOGIN_GLOBAL_MAX once.
func NewLoginGuard() *LoginGuard {
	return &LoginGuard{w: New(LoginUserMax, LoginWindow), ipMax: IPMax(LoginIPMax), globalMax: GlobalMax(LoginGlobalMax)}
}

// SetClock swaps the time source (tests).
func (g *LoginGuard) SetClock(now func() time.Time) { g.w.SetClock(now) }

// Limits are the ceilings in force (per username, per address, global).
func (g *LoginGuard) Limits() (user, ip, global int) { return LoginUserMax, g.ipMax, g.globalMax }

func loginUserKey(username string) string {
	return "u:" + strings.ToLower(strings.TrimSpace(username))
}

func loginIPKey(ip string) string { return "ip:" + strings.TrimSpace(ip) }

// Check reports whether a login for username from ip may be tried, and when
// to come back if not. ip may be empty (unknown): only the username and the
// valve count then.
func (g *LoginGuard) Check(username, ip string) (ok bool, retryAfter time.Duration) {
	uk := loginUserKey(username)
	if !g.w.Allow(uk) {
		return false, g.w.RetryAfter(uk)
	}
	ipHits := 0
	if strings.TrimSpace(ip) != "" {
		ik := loginIPKey(ip)
		if !g.w.AllowN(ik, g.ipMax) {
			return false, g.w.RetryAfterN(ik, g.ipMax)
		}
		ipHits = g.w.Count(ik)
	}
	if !g.w.AllowN(loginGlobalKey, g.globalMax) && (ipHits > 0 || g.w.Count(uk) > 0) {
		return false, g.w.RetryAfterN(loginGlobalKey, g.globalMax)
	}
	return true, 0
}

// Fail records one failed login.
func (g *LoginGuard) Fail(username, ip string) {
	g.w.Add(loginUserKey(username))
	if strings.TrimSpace(ip) != "" {
		g.w.Add(loginIPKey(ip))
	}
	g.w.Add(loginGlobalKey)
}

// Success forgets the username's failures (the address keeps its own: one
// good login must not buy a sprayer a fresh allowance).
func (g *LoginGuard) Success(username string) { g.w.Reset(loginUserKey(username)) }
