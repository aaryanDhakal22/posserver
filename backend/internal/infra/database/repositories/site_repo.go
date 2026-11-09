package repositories

import (
	"context"
	"errors"
	"fmt"

	"quiccpos/main/internal/domain/site"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SiteRepository persists the storefront's platform keys, each store's
// website hosts and its site settings (weborder stage 2).
type SiteRepository struct {
	pool *pgxpool.Pool
}

func NewSiteRepository(pool *pgxpool.Pool) *SiteRepository { return &SiteRepository{pool: pool} }

func platformKeyOf(r models.PlatformKey) site.PlatformKey {
	return site.PlatformKey{ID: idOf(r.ID), Name: r.Name, KeyPrefix: r.KeyPrefix, RevokedAt: tsPtr(r.RevokedAt),
		LastUsedAt: tsPtr(r.LastUsedAt), CreatedAt: r.CreatedAt.Time}
}

func domainOf(r models.SiteDomain) site.Domain {
	return site.Domain{Host: r.Host, TenantID: idOf(r.TenantID), LocationID: idOf(r.LocationID), Primary: r.IsPrimary,
		Verified: r.Verified, CreatedAt: r.CreatedAt.Time}
}

func (r *SiteRepository) CreateKey(ctx context.Context, name, hash, prefix string) (site.PlatformKey, error) {
	row, err := models.New(r.pool).CreatePlatformKey(ctx, models.CreatePlatformKeyParams{Name: name, KeyHash: hash, KeyPrefix: prefix})
	if err != nil {
		return site.PlatformKey{}, fmt.Errorf("create platform key: %w", err)
	}
	return platformKeyOf(row), nil
}

func (r *SiteRepository) ListKeys(ctx context.Context) ([]site.PlatformKey, error) {
	rows, err := models.New(r.pool).ListPlatformKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]site.PlatformKey, 0, len(rows))
	for _, x := range rows {
		out = append(out, platformKeyOf(x))
	}
	return out, nil
}

// RevokeKey stops a key (pgx.ErrNoRows = unknown id).
func (r *SiteRepository) RevokeKey(ctx context.Context, id uuid.UUID) (site.PlatformKey, error) {
	row, err := models.New(r.pool).RevokePlatformKey(ctx, pgID(id))
	if err != nil {
		return site.PlatformKey{}, err
	}
	return platformKeyOf(row), nil
}

// AuthenticateKey is the active key with this hash (pgx.ErrNoRows = none).
func (r *SiteRepository) AuthenticateKey(ctx context.Context, hash string) (site.PlatformKey, error) {
	row, err := models.New(r.pool).AuthenticatePlatformKey(ctx, hash)
	if err != nil {
		return site.PlatformKey{}, err
	}
	return platformKeyOf(row), nil
}

func (r *SiteRepository) TouchKey(ctx context.Context, id uuid.UUID) error {
	return models.New(r.pool).TouchPlatformKey(ctx, pgID(id))
}

// AddDomain stores a host; site.ErrHostTaken when any store has it already.
// A primary host takes over from the store's current primary.
func (r *SiteRepository) AddDomain(ctx context.Context, d site.Domain) (site.Domain, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return site.Domain{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := models.New(tx)
	if d.Primary {
		if err := q.ClearSitePrimary(ctx, pgID(d.LocationID)); err != nil {
			return site.Domain{}, err
		}
	}
	row, err := q.InsertSiteDomain(ctx, models.InsertSiteDomainParams{Host: d.Host, TenantID: pgID(d.TenantID),
		LocationID: pgID(d.LocationID), IsPrimary: d.Primary, Verified: d.Verified})
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return site.Domain{}, site.ErrHostTaken
	}
	if err != nil {
		return site.Domain{}, fmt.Errorf("add site domain: %w", err)
	}
	return domainOf(row), tx.Commit(ctx)
}

func (r *SiteRepository) Domains(ctx context.Context, locationID uuid.UUID) ([]site.Domain, error) {
	rows, err := models.New(r.pool).ListSiteDomains(ctx, pgID(locationID))
	if err != nil {
		return nil, err
	}
	out := make([]site.Domain, 0, len(rows))
	for _, x := range rows {
		out = append(out, domainOf(x))
	}
	return out, nil
}

// Domain is the row for a host (pgx.ErrNoRows = none).
func (r *SiteRepository) Domain(ctx context.Context, host string) (site.Domain, error) {
	row, err := models.New(r.pool).GetSiteDomain(ctx, host)
	if err != nil {
		return site.Domain{}, err
	}
	return domainOf(row), nil
}

// RemoveDomain deletes one of a store's hosts (false = not that store's).
func (r *SiteRepository) RemoveDomain(ctx context.Context, locationID uuid.UUID, host string) (bool, error) {
	n, err := models.New(r.pool).DeleteSiteDomain(ctx, models.DeleteSiteDomainParams{Host: host, LocationID: pgID(locationID)})
	return n > 0, err
}

// SetPrimary makes host the store's primary host (false = not that store's).
func (r *SiteRepository) SetPrimary(ctx context.Context, locationID uuid.UUID, host string) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := models.New(tx)
	if err := q.ClearSitePrimary(ctx, pgID(locationID)); err != nil {
		return false, err
	}
	n, err := q.SetSitePrimary(ctx, models.SetSitePrimaryParams{Host: host, LocationID: pgID(locationID)})
	if err != nil || n == 0 {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// SetVerified marks a host verified or not (pgx.ErrNoRows = unknown host).
func (r *SiteRepository) SetVerified(ctx context.Context, host string, verified bool) (site.Domain, error) {
	row, err := models.New(r.pool).SetSiteDomainVerified(ctx, models.SetSiteDomainVerifiedParams{Host: host, Verified: verified})
	if err != nil {
		return site.Domain{}, err
	}
	return domainOf(row), nil
}

// Settings is the store's site settings (version 0 + empty when none yet).
func (r *SiteRepository) Settings(ctx context.Context, locationID uuid.UUID) (site.Settings, error) {
	row, err := models.New(r.pool).GetSiteSettings(ctx, pgID(locationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return site.Settings{LocationID: locationID, Draft: []byte("{}"), Published: []byte("{}")}, nil
	}
	if err != nil {
		return site.Settings{}, err
	}
	return site.Settings{LocationID: locationID, Draft: row.Draft, Published: row.Published, Version: row.Version,
		UpdatedAt: row.UpdatedAt.Time}, nil
}
