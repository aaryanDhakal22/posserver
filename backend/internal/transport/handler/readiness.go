package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// ReadinessHandler answers "is this main/ actually serving traffic right
// now". Distinct from /health: /health says "the binary is up"; /ready also
// says "Postgres is reachable AND SQS responds to a probe".
//
// Used by container orchestration readiness probes (or any load balancer
// that distinguishes liveness from readiness). The default health-probe
// timeout on a k8s/docker stack is 1s; both inner checks are bounded to
// 750ms so a single slow component doesn't trip the probe before it can
// answer.
type ReadinessHandler struct {
	pool     *pgxpool.Pool
	sqs      *sqs.Client
	queueURL string
	logger   zerolog.Logger
}

func NewReadinessHandler(pool *pgxpool.Pool, sqsClient *sqs.Client, queueURL string, logger zerolog.Logger) *ReadinessHandler {
	return &ReadinessHandler{
		pool:     pool,
		sqs:      sqsClient,
		queueURL: queueURL,
		logger:   logger.With().Str("module", "ready-handler").Logger(),
	}
}

type readinessCheck struct {
	OK    bool   `json:"ok"`
	Err   string `json:"error,omitempty"`
	MS    int64  `json:"latency_ms"`
}

type readinessResponse struct {
	Ready bool           `json:"ready"`
	DB    readinessCheck `json:"db"`
	SQS   readinessCheck `json:"sqs"`
}

// GET /api/v1/ready — overall 200 if both checks pass; 503 if either fails.
// The body always carries the per-check detail so an operator can see which
// one is broken without checking logs.
func (h *ReadinessHandler) Check(c *echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), 1500*time.Millisecond)
	defer cancel()

	resp := readinessResponse{
		DB:  h.checkDB(ctx),
		SQS: h.checkSQS(ctx),
	}
	resp.Ready = resp.DB.OK && resp.SQS.OK

	status := http.StatusOK
	if !resp.Ready {
		status = http.StatusServiceUnavailable
		h.logger.Warn().Ctx(c.Request().Context()).
			Bool("db_ok", resp.DB.OK).Bool("sqs_ok", resp.SQS.OK).
			Str("db_err", resp.DB.Err).Str("sqs_err", resp.SQS.Err).
			Msg("readiness check failed")
	}
	return c.JSON(status, resp)
}

func (h *ReadinessHandler) checkDB(ctx context.Context) readinessCheck {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	if err := h.pool.Ping(ctx); err != nil {
		return readinessCheck{OK: false, Err: err.Error(), MS: time.Since(start).Milliseconds()}
	}
	return readinessCheck{OK: true, MS: time.Since(start).Milliseconds()}
}

func (h *ReadinessHandler) checkSQS(ctx context.Context) readinessCheck {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	_, err := h.sqs.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(h.queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameApproximateNumberOfMessages},
	})
	if err != nil {
		return readinessCheck{OK: false, Err: err.Error(), MS: time.Since(start).Milliseconds()}
	}
	return readinessCheck{OK: true, MS: time.Since(start).Milliseconds()}
}
