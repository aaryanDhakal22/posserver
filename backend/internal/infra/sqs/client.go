package sqsconsumer

import (
	"context"
	config "quiccpos/main/internal/shared/config"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	awscred "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/rs/zerolog"
)

func NewSQSClient(ctx context.Context, cfg *config.Config, logger zerolog.Logger) (*sqs.Client, error) {
	// Set Logger
	sqsLogger := logger.With().Str("module", "sqs").Logger()

	// Set Default Region as options
	opts := []func(*awscfg.LoadOptions) error{
		awscfg.WithRegion(cfg.SQSConfig.Region),
	}
	sqsLogger.Debug().Msg("Region was set")

	// Set the dev credentials if in dev mode
	sqsLogger.Debug().Msg("Setting credentials")
	if cfg.AppConfig.AppEnv == "dev" {
		sqsLogger.Info().Msg("Setting Dev Credentials")
		opts = append(opts, awscfg.WithCredentialsProvider(awscred.NewStaticCredentialsProvider("dev", "dev", "")))
	}

	// Load AWS Config ( sets dev cred if in dev mode )
	sqsLogger.Debug().Msg("Loading AWS Config")
	newCfg, err := awscfg.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		sqsLogger.Fatal().Err(err).Msg("Failed to load AWS config")
		return nil, err
	}

	// Create SQS Client
	sqsLogger.Debug().Msg("Creating SQS Client")
	sqsClient := sqs.NewFromConfig(newCfg, func(o *sqs.Options) {
		if cfg.AppConfig.AppEnv == "dev" {
			if cfg.SQSConfig.Endpoint != "" {
				o.BaseEndpoint = awssdk.String(cfg.SQSConfig.Endpoint)
			}
		}
	})
	return sqsClient, nil
}
