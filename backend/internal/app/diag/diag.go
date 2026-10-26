// Package diag is main's side of tablet diagnostics (main 2.0.1): Quibase's
// troubleshooting guides conclude with a report v1 that reaches main either
// through the store's Pi (RPC `diag.report`, via=pi) or — when the Pi is the
// problem — straight from the tablet with its own write-only report token
// (POST /store/v1/tablet/diag-reports, X-Quibase-Report-Key, via=direct).
//
//   - Tokens: minted through the tablet's agent (agent key), one active per
//     (location, tablet_id), at most 10 active per location. They are not tied
//     to the agent device, so linking a new Pi keeps them — which is the point:
//     the tablet must still reach main when the Pi is dead.
//   - Reports: capped + deduped on the client-generated id; rate-limited;
//     pruned after 180 days.
//   - Paging: the OWNER only (a separate pager, never the boss), throttled to
//     one page per (location, scenario, outcome) per 30 min.
package diag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/infra/database/models"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/observability"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// MsgDiagReport is the agent's RPC report carrying a tablet's report (via=pi).
const MsgDiagReport = "diag.report"

// How a report reached main (diag_reports.via).
const (
	ViaPi     = "pi"
	ViaDirect = "direct"
)

const (
	// Retention is how long reports are kept.
	Retention = 180 * 24 * time.Hour
	// PageThrottle is the minimum gap between two pages for the same
	// (location, scenario, outcome).
	PageThrottle = 30 * time.Minute
	// MaxActiveTokens is the cap on active report tokens per location (the
	// oldest is revoked).
	MaxActiveTokens = 10
	// touchEvery bounds how often a token's last_used_at is written.
	touchEvery = time.Minute
)

var (
	ErrInvalidToken = errors.New("invalid or revoked report key")
	ErrBadTablet    = errors.New("tablet_id is required")
	ErrNotFound     = errors.New("not found")
	ErrAmbiguous    = errors.New("id prefix matches more than one report")
	ErrRateLimited  = errors.New("too many diagnostics reports")
)

// RateLimitError is ErrRateLimited with how long to wait (→ 429 Retry-After).
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s — retry in %s", ErrRateLimited, e.RetryAfter.Round(time.Second))
}
func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// Queries is the slice of the sqlc models the service uses (*models.Queries
// satisfies it; unit tests use a fake).
type Queries interface {
	RevokeTabletTokensForTablet(ctx context.Context, arg models.RevokeTabletTokensForTabletParams) (int64, error)
	InsertTabletReportToken(ctx context.Context, arg models.InsertTabletReportTokenParams) (pgtype.UUID, error)
	RevokeExcessTabletTokens(ctx context.Context, arg models.RevokeExcessTabletTokensParams) (int64, error)
	GetActiveTabletTokenByHash(ctx context.Context, tokenHash string) (models.GetActiveTabletTokenByHashRow, error)
	TouchTabletToken(ctx context.Context, id pgtype.UUID) error
	ListTabletTokens(ctx context.Context, locationID pgtype.UUID) ([]models.ListTabletTokensRow, error)
	RevokeTabletTokenByID(ctx context.Context, id pgtype.UUID) (int64, error)
	DiagReportExists(ctx context.Context, id pgtype.UUID) (bool, error)
	InsertDiagReport(ctx context.Context, arg models.InsertDiagReportParams) (int64, error)
	LastDiagPage(ctx context.Context, arg models.LastDiagPageParams) (pgtype.Timestamptz, error)
	MarkDiagReportPaged(ctx context.Context, id pgtype.UUID) error
	ListDiagReports(ctx context.Context, arg models.ListDiagReportsParams) ([]models.ListDiagReportsRow, error)
	GetDiagReport(ctx context.Context, id pgtype.UUID) (models.GetDiagReportRow, error)
	FindDiagReportIDsByPrefix(ctx context.Context, prefix string) ([]pgtype.UUID, error)
	SetDiagReportResolved(ctx context.Context, arg models.SetDiagReportResolvedParams) (int64, error)
	PruneDiagReports(ctx context.Context, receivedAt pgtype.Timestamptz) (int64, error)
}

// Pager sends the owner page (*pushover.Client).
type Pager interface {
	Enabled() bool
	Send(ctx context.Context, title, message, sound string) error
}

// TenantNamer looks up a store's name for the page title (*tenancy.Service).
type TenantNamer interface {
	GetTenant(ctx context.Context, id uuid.UUID) (*tenancy.Tenant, error)
}

type Service struct {
	q       Queries
	tx      func(ctx context.Context, fn func(q Queries) error) error
	pager   Pager
	tenants TenantNamer
	limits  *limits
	logger  zerolog.Logger
	now     func() time.Time

	pageMu sync.Mutex // serialises throttle check + page + mark
	// async runs a background job (the page); tests run it inline.
	async func(fn func())

	received metric.Int64Counter
	pages    metric.Int64Counter
}

// New wires the service on the pool. pager pages the owner only.
func New(pool *pgxpool.Pool, pager Pager, tenants TenantNamer, logger zerolog.Logger) *Service {
	return newService(models.New(pool), func(ctx context.Context, fn func(q Queries) error) error {
		return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return fn(models.New(tx)) })
	}, pager, tenants, logger)
}

func newService(q Queries, tx func(context.Context, func(Queries) error) error, pager Pager, tenants TenantNamer, logger zerolog.Logger) *Service {
	m := otel.Meter(observability.MeterName)
	s := &Service{
		q: q, tx: tx, pager: pager, tenants: tenants,
		limits: newLimits(),
		logger: logger.With().Str("module", "diag").Logger(),
		now:    time.Now,
		async:  func(fn func()) { go fn() },
	}
	s.received, _ = m.Int64Counter("diag.reports.received", metric.WithDescription("Tablet diagnostics reports received (by via/kind/outcome/result)"))
	s.pages, _ = m.Int64Counter("diag.pages", metric.WithDescription("Owner pages sent for tablet diagnostics reports"))
	return s
}

func pgUUID(u uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: u, Valid: true} }

func fromPg(u pgtype.UUID) uuid.UUID {
	if !u.Valid {
		return uuid.Nil
	}
	return uuid.UUID(u.Bytes)
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// --- reports -----------------------------------------------------------------

// Store saves a parsed report for scope. Returns (id, duplicate, err); a
// report whose id is already stored is a duplicate (and costs no rate-limit
// budget, so a tablet's outbox retry always settles). Pages the owner in the
// background when ShouldPage says so.
func (s *Service) Store(ctx context.Context, scope sseBroker.Scope, r *Report, via, tabletID, tabletName string) (uuid.UUID, bool, error) {
	attrs := func(result string) metric.MeasurementOption {
		return observability.WithStore(ctx, scope.TenantID, attribute.String("via", via), attribute.String("kind", r.Kind),
			attribute.String("outcome", r.Outcome), attribute.String("result", result))
	}
	if via != ViaPi && via != ViaDirect {
		return uuid.Nil, false, fmt.Errorf("%w: via must be pi or direct", ErrBadReport)
	}
	tabletID = clip(tabletID, maxTabletID)
	if tabletName = clip(tabletName, maxTabletName); tabletName == "" {
		tabletName = r.TabletName
	}

	exists, err := s.q.DiagReportExists(ctx, pgUUID(r.ID))
	if err != nil {
		return uuid.Nil, false, err
	}
	if exists {
		s.received.Add(ctx, 1, attrs("duplicate"))
		return r.ID, true, nil
	}
	if wait := s.limits.take(scope.LocationID, tabletID, r.Kind); wait > 0 {
		s.received.Add(ctx, 1, attrs("limited"))
		return uuid.Nil, false, &RateLimitError{RetryAfter: wait}
	}

	doc, err := r.JSON()
	if err != nil {
		return uuid.Nil, false, err
	}
	n, err := s.q.InsertDiagReport(ctx, models.InsertDiagReportParams{
		ID: pgUUID(r.ID), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID),
		TabletID: tabletID, TabletName: tabletName, Via: via,
		Kind: r.Kind, Scenario: r.Scenario, Outcome: r.Outcome, Headline: r.Headline,
		Cause: r.Cause, FixedBy: r.FixedBy, AppVersion: r.AppVersion, AgentVersion: r.AgentVersion,
		Report: doc, CreatedAt: ts(r.CreatedAt),
	})
	if err != nil {
		return uuid.Nil, false, err
	}
	if n == 0 { // lost a race with the same id
		s.received.Add(ctx, 1, attrs("duplicate"))
		return r.ID, true, nil
	}
	s.received.Add(ctx, 1, attrs("stored"))
	s.logger.Info().Ctx(ctx).Str("report_id", r.ID.String()).Str("location_id", scope.LocationID.String()).
		Str("via", via).Str("kind", r.Kind).Str("scenario", r.Scenario).Str("outcome", r.Outcome).
		Msg("diagnostics report stored")

	if ShouldPage(r) {
		pctx := context.WithoutCancel(ctx)
		s.async(func() { s.page(pctx, scope, r, via, tabletName) })
	}
	return r.ID, false, nil
}

// page sends the owner page unless the same (location, scenario, outcome)
// paged within PageThrottle, then stamps paged_at. With no pager configured
// (dev) nothing is sent or stamped.
func (s *Service) page(ctx context.Context, scope sseBroker.Scope, r *Report, via, tabletName string) {
	if s.pager == nil || !s.pager.Enabled() {
		s.logger.Debug().Str("report_id", r.ID.String()).Msg("diag page skipped: no owner pager configured")
		return
	}
	s.pageMu.Lock()
	defer s.pageMu.Unlock()
	last, err := s.q.LastDiagPage(ctx, models.LastDiagPageParams{
		LocationID: pgUUID(scope.LocationID), Scenario: r.Scenario, Outcome: r.Outcome,
	})
	if err != nil {
		s.logger.Warn().Err(err).Msg("diag page: throttle lookup failed")
		return
	}
	if last.Valid && s.now().Sub(last.Time) < PageThrottle {
		s.logger.Info().Str("report_id", r.ID.String()).Msg("diag page throttled (same scenario + outcome paged recently)")
		return
	}
	store := "Store"
	if t, err := s.tenants.GetTenant(ctx, scope.TenantID); err == nil && t.Name != "" {
		store = t.Name
	}
	title, msg := FormatPage(store, r, via, tabletName)
	if err := s.pager.Send(ctx, title, msg, ""); err != nil {
		s.logger.Warn().Err(err).Str("report_id", r.ID.String()).Msg("diag page failed")
		return
	}
	s.pages.Add(ctx, 1, observability.WithStore(ctx, scope.TenantID, attribute.String("kind", r.Kind), attribute.String("outcome", r.Outcome)))
	if err := s.q.MarkDiagReportPaged(ctx, pgUUID(r.ID)); err != nil {
		s.logger.Warn().Err(err).Msg("diag page: mark paged failed")
	}
}

// rpcBody is the agent's diag.report payload.
type rpcBody struct {
	TabletID   string          `json:"tablet_id"`
	TabletName string          `json:"tablet_name"`
	Report     json.RawMessage `json:"report"`
	Pi         json.RawMessage `json:"pi"`
}

// HandleRPC is the rpc.InboundHandler for diag.report (via=pi):
// {tablet_id, tablet_name, report:{…v1…}, pi:{version, health:{…}}} →
// {id, stored, duplicate} (+ rate_limited/retry_after when limited).
func (s *Service) HandleRPC(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b rpcBody
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("%w: diag.report: %v", rpc.ErrBadPayload, err)
	}
	if len(b.Report) == 0 {
		return nil, fmt.Errorf("%w: diag.report needs a report", rpc.ErrBadPayload)
	}
	r, err := ParseReport(b.Report)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", rpc.ErrBadPayload, err)
	}
	if len(b.Pi) > 0 && string(b.Pi) != "null" {
		var pi map[string]any
		dec := json.NewDecoder(bytes.NewReader(b.Pi))
		dec.UseNumber()
		if err := dec.Decode(&pi); err != nil {
			return nil, fmt.Errorf("%w: diag.report pi must be an object", rpc.ErrBadPayload)
		}
		r.MergePi(pi)
	}
	id, dup, err := s.Store(ctx, scope, r, ViaPi, b.TabletID, b.TabletName)
	var rl *RateLimitError
	if errors.As(err, &rl) {
		return map[string]any{"id": r.ID, "stored": false, "duplicate": false,
			"rate_limited": true, "retry_after": RetrySeconds(rl.RetryAfter)}, nil
	}
	if errors.Is(err, ErrBadReport) {
		return nil, fmt.Errorf("%w: %v", rpc.ErrBadPayload, err)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "stored": !dup, "duplicate": dup}, nil
}

// RetrySeconds rounds a wait up to whole seconds (≥1) for Retry-After.
func RetrySeconds(d time.Duration) int {
	n := int((d + time.Second - 1) / time.Second)
	if n < 1 {
		n = 1
	}
	return n
}

// RunPrune deletes reports older than Retention now and then daily, until ctx ends.
func (s *Service) RunPrune(ctx context.Context) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		n, err := s.q.PruneDiagReports(ctx, ts(s.now().Add(-Retention)))
		switch {
		case err != nil && ctx.Err() == nil:
			s.logger.Warn().Err(err).Msg("diag prune failed")
		case n > 0:
			s.logger.Info().Int64("deleted", n).Msg("old diagnostics reports pruned")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
