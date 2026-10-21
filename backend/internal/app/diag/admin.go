package diag

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Summary is one report row for the admin list.
type Summary struct {
	ID           uuid.UUID  `json:"id"`
	TenantID     uuid.UUID  `json:"tenant_id"`
	LocationID   uuid.UUID  `json:"location_id"`
	StoreName    string     `json:"store_name"`
	TabletID     string     `json:"tablet_id"`
	TabletName   string     `json:"tablet_name"`
	Via          string     `json:"via"`
	Kind         string     `json:"kind"`
	Scenario     string     `json:"scenario"`
	Outcome      string     `json:"outcome"`
	Headline     string     `json:"headline"`
	Cause        string     `json:"cause"`
	FixedBy      string     `json:"fixed_by"`
	AppVersion   string     `json:"app_version"`
	AgentVersion string     `json:"agent_version"`
	CreatedAt    time.Time  `json:"created_at"`
	ReceivedAt   time.Time  `json:"received_at"`
	PagedAt      *time.Time `json:"paged_at"`
	ResolvedAt   *time.Time `json:"resolved_at"`
}

// Detail is a summary plus the full stored report.
type Detail struct {
	Summary
	Report json.RawMessage `json:"report"`
}

// Filter narrows the admin list. Zero values mean "any".
type Filter struct {
	TenantID   *uuid.UUID
	Kind       string
	Outcome    string
	Unresolved bool
	Limit      int
}

const (
	defaultListLimit = 100
	maxListLimit     = 500
)

func optText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

// List returns report summaries, newest first.
func (s *Service) List(ctx context.Context, f Filter) ([]Summary, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	var tenant pgtype.UUID
	if f.TenantID != nil {
		tenant = pgUUID(*f.TenantID)
	}
	rows, err := s.q.ListDiagReports(ctx, models.ListDiagReportsParams{
		TenantID: tenant, Kind: optText(f.Kind), Outcome: optText(f.Outcome),
		Unresolved: f.Unresolved, MaxRows: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(rows))
	for _, r := range rows {
		out = append(out, Summary{
			ID: fromPg(r.ID), TenantID: fromPg(r.TenantID), LocationID: fromPg(r.LocationID), StoreName: r.StoreName,
			TabletID: r.TabletID, TabletName: r.TabletName, Via: r.Via, Kind: r.Kind, Scenario: r.Scenario,
			Outcome: r.Outcome, Headline: r.Headline, Cause: r.Cause, FixedBy: r.FixedBy,
			AppVersion: r.AppVersion, AgentVersion: r.AgentVersion,
			CreatedAt: r.CreatedAt.Time, ReceivedAt: r.ReceivedAt.Time,
			PagedAt: tsPtr(r.PagedAt), ResolvedAt: tsPtr(r.ResolvedAt),
		})
	}
	return out, nil
}

// idPrefix is the short id a page links with (≥8 hex chars, maybe dashed).
var idPrefix = regexp.MustCompile(`^[0-9a-f-]{8,36}$`)

// Get returns one report by full uuid or by the ≥8-char prefix a page links
// with. ErrNotFound / ErrAmbiguous.
func (s *Service) Get(ctx context.Context, idOrPrefix string) (*Detail, error) {
	idOrPrefix = strings.ToLower(strings.TrimSpace(idOrPrefix))
	id, err := uuid.Parse(idOrPrefix)
	if err != nil {
		if !idPrefix.MatchString(idOrPrefix) {
			return nil, ErrNotFound
		}
		ids, err := s.q.FindDiagReportIDsByPrefix(ctx, idOrPrefix)
		if err != nil {
			return nil, err
		}
		switch len(ids) {
		case 0:
			return nil, ErrNotFound
		case 1:
			id = fromPg(ids[0])
		default:
			return nil, ErrAmbiguous
		}
	}
	r, err := s.q.GetDiagReport(ctx, pgUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &Detail{
		Summary: Summary{
			ID: fromPg(r.ID), TenantID: fromPg(r.TenantID), LocationID: fromPg(r.LocationID), StoreName: r.StoreName,
			TabletID: r.TabletID, TabletName: r.TabletName, Via: r.Via, Kind: r.Kind, Scenario: r.Scenario,
			Outcome: r.Outcome, Headline: r.Headline, Cause: r.Cause, FixedBy: r.FixedBy,
			AppVersion: r.AppVersion, AgentVersion: r.AgentVersion,
			CreatedAt: r.CreatedAt.Time, ReceivedAt: r.ReceivedAt.Time,
			PagedAt: tsPtr(r.PagedAt), ResolvedAt: tsPtr(r.ResolvedAt),
		},
		Report: r.Report,
	}, nil
}

// SetResolved marks a report resolved (keeping the first resolve time) or
// reopens it, and returns it.
func (s *Service) SetResolved(ctx context.Context, id uuid.UUID, resolved bool) (*Detail, error) {
	n, err := s.q.SetDiagReportResolved(ctx, models.SetDiagReportResolvedParams{ID: pgUUID(id), Resolved: resolved})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	return s.Get(ctx, id.String())
}
