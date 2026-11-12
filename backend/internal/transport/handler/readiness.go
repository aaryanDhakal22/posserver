package handler

import (
	"context"
	"net/http"
	"sync"
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

	// The probe is open to anyone, and each run costs a Postgres ping and an
	// AWS call (security review): the last answer is reused for cache. probe
	// and now are the checks and the clock (swapped in tests).
	cache time.Duration
	probe func(ctx context.Context) (db, sqs error)
	now   func() time.Time
	mu    sync.Mutex
	last  *readinessResponse
	at    time.Time
}

// NewReadinessHandler builds the probe; cache is how long an answer is
// reused (READY_CACHE_SECONDS; 0 = every request checks).
func NewReadinessHandler(pool *pgxpool.Pool, sqsClient *sqs.Client, queueURL string, cache time.Duration, logger zerolog.Logger) *ReadinessHandler {
	h := &ReadinessHandler{
		pool:     pool,
		sqs:      sqsClient,
		queueURL: queueURL,
		cache:    cache,
		now:      time.Now,
		logger:   logger.With().Str("module", "ready-handler").Logger(),
	}
	h.probe = h.probeDeps
	return h
}

type readinessCheck struct {
	OK  bool   `json:"ok"`
	Err string `json:"error,omitempty"`
	MS  int64  `json:"latency_ms"`
}

type readinessResponse struct {
	Ready bool           `json:"ready"`
	DB    readinessCheck `json:"db"`
	SQS   readinessCheck `json:"sqs"`
}

// GET /api/v1/ready — overall 200 if both checks pass; 503 if either fails.
// The body says which check is broken, never why: the driver's error text
// (hosts, ARNs, account ids) goes to the log only. An answer is reused for
// the cache window, so a flood of probes is one check, not one per request.
func (h *ReadinessHandler) Check(c *echo.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.last == nil || h.cache <= 0 || h.now().Sub(h.at) >= h.cache {
		// Detached from the caller: a probe that hangs up early must not
		// leave a half-done answer in the cache.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request().Context()), 1500*time.Millisecond)
		resp := h.run(ctx)
		cancel()
		h.last, h.at = &resp, h.now()
	}
	status := http.StatusOK
	if !h.last.Ready {
		status = http.StatusServiceUnavailable
	}
	return c.JSON(status, h.last)
}

// readinessUnavailable is all a caller learns about a failed check.
const readinessUnavailable = "unavailable"

func (h *ReadinessHandler) run(ctx context.Context) readinessResponse {
	start := time.Now()
	dbErr, sqsErr := h.probe(ctx)
	ms := time.Since(start).Milliseconds()
	check := func(err error) readinessCheck {
		if err != nil {
			return readinessCheck{OK: false, Err: readinessUnavailable, MS: ms}
		}
		return readinessCheck{OK: true, MS: ms}
	}
	resp := readinessResponse{DB: check(dbErr), SQS: check(sqsErr)}
	resp.Ready = resp.DB.OK && resp.SQS.OK
	if !resp.Ready {
		ev := h.logger.Warn().Ctx(ctx).Bool("db_ok", resp.DB.OK).Bool("sqs_ok", resp.SQS.OK)
		if dbErr != nil {
			ev = ev.Str("db_err", dbErr.Error())
		}
		if sqsErr != nil {
			ev = ev.Str("sqs_err", sqsErr.Error())
		}
		ev.Msg("readiness check failed")
	}
	return resp
}

// probeDeps pings Postgres and pokes SQS side by side, each bounded to 750 ms.
func (h *ReadinessHandler) probeDeps(ctx context.Context) (dbErr, sqsErr error) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		defer cancel()
		dbErr = h.pool.Ping(ctx)
	}()
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		defer cancel()
		_, sqsErr = h.sqs.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
			QueueUrl:       aws.String(h.queueURL),
			AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameApproximateNumberOfMessages},
		})
	}()
	wg.Wait()
	return dbErr, sqsErr
}
