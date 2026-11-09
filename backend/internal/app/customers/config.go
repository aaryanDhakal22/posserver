package customers

import (
	"os"
	"strings"

	"quiccpos/main/internal/infra/twilioverify"

	"github.com/rs/zerolog"
)

// CodesFromEnv picks how sign-in codes are sent:
//
//	SIGNIN_CODES  twilio | fake | off   (unset: fake on dev / staging, off in prod)
//	TWILIO_ACCOUNT_SID / TWILIO_AUTH_TOKEN / TWILIO_VERIFY_SERVICE_SID
//	TWILIO_BASE_URL                      override Verify's API host (tests)
//
// fake is refused in prod. The returned *FakeCodes is non-nil only for fake.
func CodesFromEnv(appEnv string, lg zerolog.Logger) (Codes, *FakeCodes) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("SIGNIN_CODES")))
	prod := appEnv == "prod" || appEnv == "production"
	if mode == "" {
		mode = "fake"
		if prod {
			mode = "off"
		}
	}
	switch {
	case mode == "fake" && prod:
		lg.Error().Msg("SIGNIN_CODES=fake is refused in prod — customer sign-in is off")
	case mode == "fake":
		f := NewFakeCodes(lg)
		lg.Info().Msg("sign-in codes: FAKE (logged, never sent)")
		return f, f
	case mode == "twilio":
		sid, tok, svc := os.Getenv("TWILIO_ACCOUNT_SID"), os.Getenv("TWILIO_AUTH_TOKEN"), os.Getenv("TWILIO_VERIFY_SERVICE_SID")
		if sid == "" || tok == "" || svc == "" {
			lg.Warn().Msg("SIGNIN_CODES=twilio but TWILIO_ACCOUNT_SID / TWILIO_AUTH_TOKEN / TWILIO_VERIFY_SERVICE_SID are missing — sign-in is off")
			return nil, nil
		}
		lg.Info().Msg("sign-in codes: Twilio Verify")
		return twilioverify.New(os.Getenv("TWILIO_BASE_URL"), sid, tok, svc), nil
	}
	return nil, nil
}
