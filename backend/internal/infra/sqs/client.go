package sqsconsumer

import (
	"context"

	config "quiccpos/main/internal/shared/config"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	awscred "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"
)

func NewSQSClient(ctx context.Context, cfg *config.Config, logger zerolog.Logger) (*sqs.Client, error) {
	sqsLogger := logger.With().Str("module", "sqs").Logger()

	opts := []func(*awscfg.LoadOptions) error{
		awscfg.WithRegion(cfg.SQSConfig.Region),
	}
	sqsLogger.Debug().Msg("Region was set")

	sqsLogger.Debug().Msg("Setting credentials")
	if cfg.AppConfig.AppEnv == "dev" {
		sqsLogger.Info().Msg("Setting Dev Credentials")
		opts = append(opts, awscfg.WithCredentialsProvider(awscred.NewStaticCredentialsProvider("dev", "dev", "")))
	}

	sqsLogger.Debug().Msg("Loading AWS Config")
	newCfg, err := awscfg.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		sqsLogger.Fatal().Err(err).Msg("Failed to load AWS config")
		return nil, err
	}

	// Wire otelaws: injects traceparent into SQS MessageAttributes on send
	// and extracts it on receive, so a producer-side trace from online/
	// automatically chains into main's sqs.process span. Also auto-creates
	// client-side spans for every AWS call (ReceiveMessage, DeleteMessage,
	// GetQueueAttributes …).
	otelaws.AppendMiddlewares(&newCfg.APIOptions)

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
