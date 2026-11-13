package pushover

import (
	"os"
	"strings"
	"sync/atomic"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// The prod-only guard (main 4.0.6, the owner 2026-11-13: "I don't want
// staging being able to send me notifications"). Every page main sends — the
// owner's offline alert, "Pi replaced", restore pages, tablet diagnostics,
// channel-order notices (money / Square problems), the store's alert test —
// leaves through Client.post, and post sends only when APP_ENV is "prod".
// Anywhere else (staging, dev, unset) the message is logged at info and
// dropped, even with the keys configured.
//
// PUSHOVER_NONPROD=1 is the one escape hatch, for tests only (a fake
// Pushover via PUSHOVER_API_URL). Never set it in Doppler.

// NonProdEnvVar is the tests-only escape hatch.
const NonProdEnvVar = "PUSHOVER_NONPROD"

// sendAllowed: APP_ENV=prod, or the tests-only escape hatch.
func sendAllowed(appEnv, nonProd string) bool {
	return appEnv == "prod" || strings.TrimSpace(nonProd) == "1"
}

// guardFromEnv reads APP_ENV and PUSHOVER_NONPROD (at New).
func guardFromEnv() (appEnv string, allowed bool) {
	appEnv = os.Getenv("APP_ENV")
	return appEnv, sendAllowed(appEnv, os.Getenv(NonProdEnvVar))
}

var suppressLog atomic.Pointer[zerolog.Logger]

// SetLogger sets where every client logs a suppressed message (default: the
// global zerolog logger).
func SetLogger(l zerolog.Logger) { suppressLog.Store(&l) }

func guardLogger() *zerolog.Logger {
	if l := suppressLog.Load(); l != nil {
		return l
	}
	return &log.Logger
}

// Sends reports whether this client sends at all (APP_ENV=prod or the
// tests-only PUSHOVER_NONPROD=1).
func (c *Client) Sends() bool { return c != nil && c.allowed }

// suppressed logs a message the guard dropped; true = drop it.
func (c *Client) suppressed(title, message string) bool {
	if c.allowed {
		return false
	}
	env := c.appEnv
	if env == "" {
		env = "unset"
	}
	head := title
	if head == "" {
		head, _, _ = strings.Cut(message, "\n")
		if len(head) > 80 {
			head = head[:80] + "…"
		}
	}
	guardLogger().Info().Str("app_env", env).Str("title", title).Str("text", message).
		Msgf("pushover suppressed (APP_ENV=%s): %s", env, head)
	return true
}
