package sqsconsumer

import (
	"context"
	"encoding/json"
	"errors"
	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/transport/dto"
	"time"

	orderSvc "quiccpos/main/internal/app/order"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/rs/zerolog"
)

const (
	maxBackoff     = 60 * time.Second
	initialBackoff = 2 * time.Second
)

type Consumer struct {
	client       *sqs.Client
	queueURL     string
	orderService *orderSvc.Service
	logger       zerolog.Logger
}

func NewConsumer(client *sqs.Client, queueURL string, orderService *orderSvc.Service, logger zerolog.Logger) *Consumer {
	return &Consumer{
		client:       client,
		queueURL:     queueURL,
		orderService: orderService,
		logger:       logger.With().Str("module", "sqs-consumer").Logger(),
	}
}

// Start begins the long-poll loop. Blocks until ctx is cancelled.
func (c *Consumer) Start(ctx context.Context) {
	c.logger.Info().Str("queue", c.queueURL).Msg("Starting SQS consumer")

	if err := c.probe(ctx); err != nil {
		c.logger.Fatal().Err(err).Msg("SQS connectivity probe failed — consumer will not start")
		return
	}
	c.logger.Info().Msg("SQS connectivity probe passed")

	backoff := initialBackoff

	for {
		select {
		case <-ctx.Done():
			c.logger.Info().Msg("SQS consumer stopped")
			return
		default:
		}

		output, err := c.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(c.queueURL),
			MaxNumberOfMessages: 10,
			WaitTimeSeconds:     20,
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.logAWSError("ReceiveMessage failed", err)
			c.logger.Warn().Dur("retry_in", backoff).Msg("Backing off before next poll")
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}

		// Reset backoff on success.
		backoff = initialBackoff

		c.logger.Debug().Int("count", len(output.Messages)).Msg("Poll returned messages")

		for _, msg := range output.Messages {
			c.processMessage(ctx, msg)
		}
	}
}

// probe performs a quick GetQueueAttributes call (no long-poll) to verify
// that credentials and queue permissions are working before entering the loop.
func (c *Consumer) probe(ctx context.Context) error {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	_, err := c.client.GetQueueAttributes(probeCtx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(c.queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameApproximateNumberOfMessages},
	})
	if err != nil {
		c.logAWSError("SQS probe error", err)
		return err
	}
	return nil
}

// logAWSError inspects the error type and logs actionable remediation hints.
func (c *Consumer) logAWSError(msg string, err error) {
	ev := c.logger.Error().Err(err)

	var oe *smithy.OperationError
	if errors.As(err, &oe) {
		ev = ev.Str("aws_operation", oe.Operation())

		var re *smithyhttp.ResponseError
		if errors.As(oe.Unwrap(), &re) {
			ev = ev.Int("http_status", re.HTTPStatusCode())
			switch re.HTTPStatusCode() {
			case 403:
				ev.Msg(msg + ": HTTP 403 — IAM policy does not allow this action on the queue. " +
					"Fix: attach sqs:ReceiveMessage + sqs:DeleteMessage + sqs:GetQueueAttributes to the role/user, " +
					"or check that AWS_REGION matches the queue region.")
				return
			case 404:
				ev.Msg(msg + ": HTTP 404 — queue URL not found. " +
					"Fix: verify SQS_QUEUE_URL in .env and that the queue exists in region " +
					"AWS_REGION.")
				return
			}
		}
	}

	// Credential/config errors (no HTTP response at all)
	errStr := err.Error()
	switch {
	case contains(errStr, "NoCredentialProviders", "no EC2 IMDS role found", "failed to refresh cached credentials"):
		ev.Msg(msg + ": no AWS credentials found. " +
			"Fix: mount ~/.aws into the container (volumes: - ~/.aws:/root/.aws:ro) " +
			"or set AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY env vars.")
	case contains(errStr, "ExpiredToken", "expired"):
		ev.Msg(msg + ": AWS credentials are expired. Fix: run `aws sso login` or refresh your credentials.")
	case contains(errStr, "InvalidClientTokenId", "InvalidAccessKeyId"):
		ev.Msg(msg + ": AWS access key is invalid. Fix: check AWS_ACCESS_KEY_ID / ~/.aws/credentials.")
	case contains(errStr, "context deadline exceeded", "connection refused", "no such host"):
		ev.Msg(msg + ": network error reaching AWS. Fix: check internet connectivity from the container " +
			"and verify AWS_REGION=" + "is correct.")
	default:
		ev.Msg(msg)
	}
}

func contains(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

func (c *Consumer) processMessage(ctx context.Context, msg sqstypes.Message) {
	msgID := aws.ToString(msg.MessageId)
	start := time.Now()

	if msg.Body == nil {
		c.logger.Warn().Str("message_id", msgID).Msg("received message with nil body, skipping")
		return
	}

	c.logger.Debug().Str("message_id", msgID).Msg("unmarshaling SQS envelope")

	var cmd order.CreateOrderCommand
	if err := json.Unmarshal([]byte(*msg.Body), &cmd); err != nil {
		c.logger.Error().
			Err(err).
			Str("message_id", msgID).
			Msg("failed to unmarshal SQS envelope, skipping")
		return
	}
	c.logger.Debug().Str("message_id", msgID).Str("order_id_str", cmd.OrderID).Msg("envelope parsed")

	c.logger.Debug().Str("message_id", msgID).Msg("unmarshaling order payload")
	var dtoOrder dto.Order
	if err := json.Unmarshal([]byte(cmd.Payload), &dtoOrder); err != nil {
		c.logger.Error().
			Err(err).
			Str("message_id", msgID).
			Msg("failed to unmarshal order payload, skipping")
		return
	}
	o := dtoOrder.ToDomain()

	customerName := o.Customer.FirstName + " " + o.Customer.LastName
	c.logger.Info().
		Str("message_id", msgID).
		Int("order_id", o.OrderID).
		Str("customer_name", customerName).
		Str("service_type", o.ServiceType).
		Int("item_count", len(o.Items)).
		Msg("order received from SQS")

	c.logger.Debug().Str("message_id", msgID).Int("order_id", o.OrderID).Msg("persisting order")
	if err := c.orderService.Create(ctx, &o); err != nil {
		c.logger.Error().
			Err(err).
			Str("message_id", msgID).
			Int("order_id", o.OrderID).
			Str("customer_name", customerName).
			Dur("elapsed", time.Since(start)).
			Msg("failed to persist order, leaving on queue for retry")
		return
	}
	c.logger.Debug().Str("message_id", msgID).Int("order_id", o.OrderID).Msg("order persisted, deleting from queue")

	if _, err := c.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.queueURL),
		ReceiptHandle: msg.ReceiptHandle,
	}); err != nil {
		c.logger.Error().
			Err(err).
			Str("message_id", msgID).
			Int("order_id", o.OrderID).
			Msg("failed to delete message from queue")
		return
	}

	c.logger.Info().
		Str("message_id", msgID).
		Int("order_id", o.OrderID).
		Str("customer_name", customerName).
		Dur("total_ms", time.Since(start)).
		Msg("message processed and deleted")
}
