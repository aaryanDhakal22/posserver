package receipts

import (
	"context"
	"os"
	"strings"

	"quiccpos/main/internal/infra/mail"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/rs/zerolog"
)

// DefaultFrom is the receipts' From address (the store's name is the display name).
const DefaultFrom = "orders@quiccpos.com"

// SenderFromEnv picks how receipts are sent:
//
//	MAIL_ENV           ses | fake | off   (unset: fake on dev / staging, off in prod)
//	MAIL_FROM_ADDRESS  default orders@quiccpos.com (a verified SES identity)
//	SES_REGION         default AWS_REGION, then us-east-1
//	SES_ENDPOINT       override (tests)
//
// SES uses the AWS credential chain (AWS_ACCESS_KEY_ID / _SECRET_ACCESS_KEY,
// ~/.aws). The returned *mail.Fake is non-nil only for fake; a nil Sender
// means mail is off (receipts are skipped).
func SenderFromEnv(ctx context.Context, appEnv string, lg zerolog.Logger) (mail.Sender, *mail.Fake, string) {
	from := strings.TrimSpace(os.Getenv("MAIL_FROM_ADDRESS"))
	if from == "" {
		from = DefaultFrom
	}
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("MAIL_ENV")))
	prod := appEnv == "prod" || appEnv == "production"
	if mode == "" {
		mode = "fake"
		if prod {
			mode = "off"
		}
	}
	switch {
	case mode == "fake" && prod:
		lg.Error().Msg("MAIL_ENV=fake is refused in prod — receipts are off")
	case mode == "fake":
		lg.Info().Msg("receipts: FAKE mail (kept in memory)")
		f := mail.NewFake()
		return f, f, from
	case mode == "ses":
		region := os.Getenv("SES_REGION")
		if region == "" {
			region = os.Getenv("AWS_REGION")
		}
		if region == "" {
			region = "us-east-1"
		}
		cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
		if err != nil {
			lg.Error().Err(err).Msg("receipts: AWS config — mail is off")
			return nil, nil, from
		}
		s := mail.NewSES(region, cfg.Credentials)
		s.Endpoint = os.Getenv("SES_ENDPOINT")
		lg.Info().Str("region", region).Str("from", from).Msg("receipts: SES")
		return s, nil, from
	}
	return nil, nil, from
}
