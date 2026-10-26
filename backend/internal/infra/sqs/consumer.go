package sqsconsumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/observability"

	orderSvc "quiccpos/main/internal/app/order"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const (
	maxBackoff     = 60 * time.Second
	initialBackoff = 2 * time.Second

	tracerName = "quiccpos/main/sqs"

	// MessageAttribute keys main/ reads on every inbound message. The
	// producer-side contract lives in online/SQS_CONTRACT.md.
	sourceAttribute     = "source"
	tenantIDAttribute   = "tenant_id"
	locationIDAttribute = "location_id"

	// defaultSource is what the consumer assumes when no `source` attribute
	// is set. Belt-and-suspenders — every producer is expected to set it.
	defaultSource = order.SourceOnline
)

// Caster is the interface every per-source caster satisfies. CastInput lives
// in domain/order so casters can implement it without importing this package.
type Caster interface {
	Source() order.Source
	Cast(ctx context.Context, in order.CastInput) (*order.Order, error)
}

type Consumer struct {
	client       *sqs.Client
	queueURL     string
	casters      map[order.Source]Caster
	orderService *orderSvc.Service
	logger       zerolog.Logger
	tracer       trace.Tracer
	meters       observability.Meters
}

// NewConsumer takes the per-source caster registry. Today only `online` is
// registered; adding `mobile` / `pos` / `kiosk` is one more entry plus a
// new caster package — no other plumbing.
func NewConsumer(client *sqs.Client, queueURL string, casters map[order.Source]Caster, orderService *orderSvc.Service, logger zerolog.Logger, meters observability.Meters) *Consumer {
	return &Consumer{
		client:       client,
		queueURL:     queueURL,
		casters:      casters,
		orderService: orderService,
		logger:       logger.With().Str("module", "sqs-consumer").Logger(),
		tracer:       otel.Tracer(tracerName),
		meters:       meters,
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
			QueueUrl:              aws.String(c.queueURL),
			MaxNumberOfMessages:   10,
			WaitTimeSeconds:       20,
			MessageAttributeNames: []string{"All"},
			// SentTimestamp → sqs.message.age (the queue wait, without CloudWatch).
			MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{
				sqstypes.MessageSystemAttributeNameSentTimestamp,
			},
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.meters.SQSReceiveErrors.Add(ctx, 1, metric.WithAttributes(
				attribute.String("reason", receiveErrorReason(err)),
			))
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

		backoff = initialBackoff

		c.logger.Debug().Int("count", len(output.Messages)).Msg("Poll returned messages")

		receivedAt := time.Now()
		for _, msg := range output.Messages {
			// Before routing the store is unknown: no store labels.
			observability.RecordLatency(ctx, c.meters.SQSMessageAge, sentTimestamp(msg), receivedAt)
		}
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

// sentTimestamp is the message's SentTimestamp system attribute (epoch
// milliseconds); zero when absent or malformed.
func sentTimestamp(msg sqstypes.Message) time.Time {
	v, ok := msg.Attributes[string(sqstypes.MessageSystemAttributeNameSentTimestamp)]
	if !ok {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// receiveErrorReason buckets a ReceiveMessage error into a small fixed set
// for the sqs.receive.errors `reason` attribute: forbidden, queue_not_found,
// throttled, server, credentials, network, other.
func receiveErrorReason(err error) string {
	var oe *smithy.OperationError
	if errors.As(err, &oe) {
		var re *smithyhttp.ResponseError
		if errors.As(oe.Unwrap(), &re) {
			switch code := re.HTTPStatusCode(); {
			case code == 403:
				return "forbidden"
			case code == 404:
				return "queue_not_found"
			case code == 429:
				return "throttled"
			case code >= 500:
				return "server"
			}
		}
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "AWS.SimpleQueueService.NonExistentQueue", "QueueDoesNotExist":
			return "queue_not_found"
		case "RequestThrottled", "ThrottlingException", "OverLimit":
			return "throttled"
		}
	}
	errStr := err.Error()
	switch {
	case contains(errStr, "NoCredentialProviders", "no EC2 IMDS role found", "failed to refresh cached credentials",
		"ExpiredToken", "InvalidClientTokenId", "InvalidAccessKeyId"):
		return "credentials"
	case contains(errStr, "context deadline exceeded", "connection refused", "no such host", "connection reset", "i/o timeout"):
		return "network"
	}
	return "other"
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
			"and verify AWS_REGION is correct.")
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

// processMessage wraps cast → persist → delete in a single sqs.process span.
// otelaws extracts traceparent from upstream MessageAttributes, which online/
// injects automatically via its otelaws-instrumented client, so this span
// chains under SQS.SendMessage without custom plumbing.
//
// On cast failure: the message is NOT deleted — SQS will redrive it and
// eventually land it in the DLQ via maxReceiveCount.
// On duplicate persistence: the message IS deleted; the order is already in
// the DB and a retry would never succeed.
func (c *Consumer) processMessage(ctx context.Context, msg sqstypes.Message) {
	msgID := aws.ToString(msg.MessageId)
	start := time.Now()

	c.meters.SQSMessagesInFlt.Add(ctx, 1)
	defer c.meters.SQSMessagesInFlt.Add(ctx, -1)

	ctx, span := c.tracer.Start(ctx, "sqs.process",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "aws_sqs"),
			attribute.String("messaging.destination", c.queueURL),
			attribute.String("messaging.message.id", msgID),
		),
	)
	defer span.End()

	if msg.Body == nil {
		span.SetStatus(codes.Error, "nil body")
		c.logger.Warn().Ctx(ctx).Str("message_id", msgID).Msg("received message with nil body, skipping")
		return
	}

	source := readSourceAttribute(msg.MessageAttributes)
	tenantID, locationID, scopeErr := scopeForSource(source, msg.MessageAttributes)
	span.SetAttributes(
		attribute.String("messaging.source", string(source)),
		attribute.String("tenant.id", uuid.UUID(tenantID).String()),
		attribute.String("location.id", uuid.UUID(locationID).String()),
	)
	if scopeErr != nil {
		// Missing or malformed tenant/location attributes — permanent config
		// failure on the producer side. Surface in metrics + DLQ via redrive.
		span.SetStatus(codes.Error, "missing_scope")
		c.meters.OrdersCastFailed.Add(ctx, 1, observability.WithUnrouted(
			attribute.String("source", string(source)),
			attribute.String("reason", "missing_scope"),
		))
		c.logger.Error().Ctx(ctx).Err(scopeErr).Str("message_id", msgID).Str("source", string(source)).
			Msg("missing/invalid tenant_id or location_id MessageAttribute; leaving for redrive/DLQ")
		return
	}

	caster, ok := c.casters[source]
	if !ok {
		span.SetStatus(codes.Error, "unknown_source")
		c.meters.OrdersCastFailed.Add(ctx, 1, observability.WithUnrouted(
			attribute.String("source", string(source)),
			attribute.String("reason", "unknown_source"),
		))
		c.logger.Error().Ctx(ctx).Str("message_id", msgID).Str("source", string(source)).
			Msg("unknown source on SQS message; no caster registered")
		return
	}

	raw := []byte(*msg.Body)
	if source == order.SourceOnline {
		if inner, ok := unwrapV1Envelope(raw); ok {
			raw = inner
			span.SetAttributes(attribute.String("messaging.envelope", "v1"))
		}
	}

	agg, err := caster.Cast(ctx, order.CastInput{
		Raw:        raw,
		TenantID:   tenantID,
		LocationID: locationID,
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "cast_failed")
		c.meters.OrdersCastFailed.Add(ctx, 1, observability.WithUnrouted(
			attribute.String("source", string(source)),
			attribute.String("reason", "cast_error"),
		))
		c.logger.Error().Ctx(ctx).Err(err).Str("message_id", msgID).Str("source", string(source)).Int("body_bytes", len(raw)).
			Msg("cast failed; leaving message on queue for redrive/DLQ")
		return
	}

	span.SetAttributes(
		attribute.String("order.id", uuid.UUID(agg.ID()).String()),
		attribute.String("order.source", string(agg.Source())),
		attribute.String("order.source_event_id", agg.SourceEventID()),
		attribute.Int("order.item_count", len(agg.Items())),
	)

	tenantUUID := uuid.UUID(agg.TenantID())
	c.meters.OrdersConsumed.Add(ctx, 1, observability.WithStore(ctx, tenantUUID,
		attribute.String("source", string(agg.Source())),
		attribute.String("service_type", string(agg.Fulfillment().Type)),
	))

	if err := c.orderService.Create(ctx, agg); err != nil {
		if errors.Is(err, order.ErrDuplicate) {
			// Already in the DB — safe to delete from the queue.
			c.deleteMessage(ctx, msg, msgID, agg.ID())
			c.meters.OrdersPersisted.Add(ctx, 1, observability.WithStore(ctx, tenantUUID, attribute.String("result", "duplicate")))
			return
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, "persist_failed")
		c.meters.OrdersPersistFailed.Add(ctx, 1, observability.WithStore(ctx, tenantUUID,
			attribute.String("source", string(agg.Source())),
		))
		c.logger.Error().Ctx(ctx).Err(err).
			Str("message_id", msgID).
			Str("order_id", uuid.UUID(agg.ID()).String()).
			Dur("elapsed", time.Since(start)).
			Msg("failed to persist order, leaving on queue for retry")
		return
	}
	c.meters.OrdersPersisted.Add(ctx, 1, observability.WithStore(ctx, tenantUUID, attribute.String("result", "ok")))
	// Only a first persist: a duplicate (redelivery) would re-measure an old order.
	observability.RecordLatency(ctx, c.meters.OrdersIngestLatency, agg.PlacedAt(), time.Now(),
		observability.WithStore(ctx, tenantUUID, attribute.String("source", string(agg.Source()))))

	c.deleteMessage(ctx, msg, msgID, agg.ID())

	c.logger.Info().Ctx(ctx).
		Str("message_id", msgID).
		Str("order_id", uuid.UUID(agg.ID()).String()).
		Str("source_event_id", agg.SourceEventID()).
		Dur("total_ms", time.Since(start)).
		Msg("message processed and deleted")
}

// unwrapV1Envelope recognises the v1 online relay's message — the vendor JSON
// wrapped as a string: {"ID", "Payload": "<vendor json>", "DateCreated",
// "CreatedAt"} (no MessageAttributes) — and returns the inner vendor body. It
// lets v2 main read the v1 queues as they are: the staging mirror for the
// shadow run, and prod's queue at cutover without changing prod online. A
// raw vendor body (it has "order_id", never a string "Payload") is left alone.
func unwrapV1Envelope(raw []byte) ([]byte, bool) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, false
	}
	if _, isVendor := env["order_id"]; isVendor {
		return nil, false
	}
	p, ok := env["Payload"]
	if !ok {
		return nil, false
	}
	var inner string
	if err := json.Unmarshal(p, &inner); err != nil || inner == "" {
		return nil, false
	}
	return []byte(inner), true
}

// readSourceAttribute pulls the `source` MessageAttribute and maps it to an
// order.Source. Empty / missing attribute → defaultSource (online), so the
// frozen online/ relay keeps working without setting attributes. Any other
// value is returned as-is, even if no caster is registered — the dispatch
// site handles the "unknown source" case explicitly.
func readSourceAttribute(attrs map[string]sqstypes.MessageAttributeValue) order.Source {
	v, ok := attrs[sourceAttribute]
	if !ok || v.StringValue == nil || *v.StringValue == "" {
		return defaultSource
	}
	return order.Source(*v.StringValue)
}

// readScopeAttributes parses tenant_id and location_id from MessageAttributes.
// Both are required — main/ refuses messages without them. The placeholder
// pair lives in the seed migration so online's default Doppler config Just
// Works without provisioning real tenants first.
// scopeForSource reads the tenant/location scope attributes. They're required
// for every source except `online`: the online caster routes by the vendor
// body's store_id/store_name and ignores the SQS scope entirely, so an online
// message without (or with malformed) scope attributes is still processed —
// online's SCOPE_TENANT_ID/SCOPE_LOCATION_ID are optional config.
func scopeForSource(source order.Source, attrs map[string]sqstypes.MessageAttributeValue) (order.TenantID, order.LocationID, error) {
	tid, lid, err := readScopeAttributes(attrs)
	if err != nil && source == order.SourceOnline {
		return order.TenantID{}, order.LocationID{}, nil
	}
	return tid, lid, err
}

func readScopeAttributes(attrs map[string]sqstypes.MessageAttributeValue) (order.TenantID, order.LocationID, error) {
	tenantStr, err := requiredStringAttr(attrs, tenantIDAttribute)
	if err != nil {
		return order.TenantID{}, order.LocationID{}, err
	}
	locationStr, err := requiredStringAttr(attrs, locationIDAttribute)
	if err != nil {
		return order.TenantID{}, order.LocationID{}, err
	}
	tenantUUID, err := uuid.Parse(tenantStr)
	if err != nil {
		return order.TenantID{}, order.LocationID{}, fmt.Errorf("invalid tenant_id: %w", err)
	}
	locationUUID, err := uuid.Parse(locationStr)
	if err != nil {
		return order.TenantID{}, order.LocationID{}, fmt.Errorf("invalid location_id: %w", err)
	}
	return order.TenantID(tenantUUID), order.LocationID(locationUUID), nil
}

func requiredStringAttr(attrs map[string]sqstypes.MessageAttributeValue, key string) (string, error) {
	v, ok := attrs[key]
	if !ok || v.StringValue == nil || *v.StringValue == "" {
		return "", fmt.Errorf("missing MessageAttribute %q", key)
	}
	return *v.StringValue, nil
}

func (c *Consumer) deleteMessage(ctx context.Context, msg sqstypes.Message, msgID string, orderID order.ID) {
	if _, err := c.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.queueURL),
		ReceiptHandle: msg.ReceiptHandle,
	}); err != nil {
		c.logger.Error().Ctx(ctx).
			Err(err).
			Str("message_id", msgID).
			Str("order_id", uuid.UUID(orderID).String()).
			Msg("failed to delete message from queue")
	}
}
