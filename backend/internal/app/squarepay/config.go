package squarepay

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"strings"

	"quiccpos/main/internal/infra/square"
	"quiccpos/main/internal/infra/square/squarefake"

	"github.com/rs/zerolog"
)

// FromEnv reads Square's settings (weborder stage 2).
//
//	SQUARE_ENV                    production | sandbox | fake | off
//	                              (unset: fake on dev, off elsewhere; fake is never allowed in prod)
//	SQUARE_APP_ID / _APP_SECRET   the Square app (sandbox or production)
//	SQUARE_TOKEN_KEY              64 hex chars: encrypts stores' Square tokens
//	SQUARE_REDIRECT_URL           the OAuth callback registered in the app (…/square/oauth/callback):
//	                              main's own, or — main on a private network — the storefront's,
//	                              which relays code + state to POST /square/oauth/forward
//	                              (DURABILITY-PLAN.md step 6). Connect Square and the code
//	                              exchange both use it.
//	SQUARE_APP_URL                where the owner lands afterwards: main's URL as the manager's
//	                              browser reaches it ("" = relative, only right when the
//	                              redirect is main's own)
//	SQUARE_WEBHOOK_SIGNATURE_KEY  + SQUARE_WEBHOOK_URL: the webhook subscription
//	SQUARE_BASE_URL               override Square's API host
//
// The fake (package squarefake) is served by main itself under /__fake/square.
func FromEnv(appEnv string, lg zerolog.Logger) (Config, http.Handler) {
	env := strings.ToLower(strings.TrimSpace(os.Getenv("SQUARE_ENV")))
	prod := appEnv == "prod" || appEnv == "production"
	if env == "" {
		env = "off"
		if appEnv == "dev" {
			env = "fake"
		}
	}
	if env == "fake" && prod {
		lg.Error().Msg("SQUARE_ENV=fake is refused in prod — Square is off")
		env = "off"
	}
	key, _ := hex.DecodeString(os.Getenv("SQUARE_TOKEN_KEY"))
	cfg := Config{Env: env, AppID: os.Getenv("SQUARE_APP_ID"), AppSecret: os.Getenv("SQUARE_APP_SECRET"),
		BaseURL: os.Getenv("SQUARE_BASE_URL"), RedirectURL: os.Getenv("SQUARE_REDIRECT_URL"), AppURL: os.Getenv("SQUARE_APP_URL"),
		TokenKey: key, WebhookKey: os.Getenv("SQUARE_WEBHOOK_SIGNATURE_KEY"), WebhookURL: os.Getenv("SQUARE_WEBHOOK_URL")}
	switch env {
	case "fake":
		local := "http://localhost:1323"
		cfg.AppID, cfg.AppSecret = "sq0idp-fake", "sq0csp-fake"
		if cfg.BaseURL == "" {
			cfg.BaseURL = local + "/__fake/square"
		}
		if cfg.RedirectURL == "" {
			cfg.RedirectURL = local + "/square/oauth/callback"
		}
		if cfg.AppURL == "" {
			// main's own origin (the fake runs inside main): absolute, so the
			// storefront's relay can send the browser back too.
			if u, err := url.Parse(cfg.BaseURL); err == nil && u.Scheme != "" && u.Host != "" {
				cfg.AppURL = u.Scheme + "://" + u.Host
			}
		}
		if len(cfg.TokenKey) != 32 {
			sum := sha256.Sum256([]byte("quicc dev fake square token key"))
			cfg.TokenKey = sum[:]
		}
		if cfg.WebhookKey == "" {
			cfg.WebhookKey, cfg.WebhookURL = "fake-webhook-key", local+"/square/webhook"
		}
		fake := squarefake.New(cfg.AppID, cfg.AppSecret)
		lg.Info().Str("base_url", cfg.BaseURL).Msg("Square: the in-process fake")
		return cfg, fake.Handler("/__fake/square", cfg.RedirectURL)
	case "sandbox", "production":
		if cfg.BaseURL == "" {
			cfg.BaseURL = square.SandboxURL
			if env == "production" {
				cfg.BaseURL = square.ProductionURL
			}
		}
		if !cfg.Enabled() {
			lg.Warn().Str("env", env).Msg("Square: SQUARE_APP_ID / SQUARE_APP_SECRET / SQUARE_TOKEN_KEY (64 hex) missing — Square is off")
		} else {
			lg.Info().Str("env", env).Msg("Square: on")
		}
		return cfg, nil
	}
	cfg.Env = "off"
	return cfg, nil
}
