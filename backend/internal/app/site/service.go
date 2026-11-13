// Package siteApp is the use-case layer for store websites (weborder stage
// 2): the storefront's platform keys (admin), each store's hosts (admin +
// store manager) and resolving a host to the store it serves.
package siteApp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	authSvc "quiccpos/main/internal/app/auth"
	"quiccpos/main/internal/domain/site"
	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/shared/touch"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// touches bounds the last-used stamps (one per id per minute, 5 s each).
var touches = touch.New(touch.Every)

var (
	ErrNotFound     = errors.New("not found")
	ErrInvalidKey   = errors.New("invalid or revoked platform key")
	ErrUnknownSite  = errors.New("no store is served on that host")
	ErrNameRequired = errors.New("name is required")
	ErrPlatformHost = errors.New("hosts on the platform's own domain are added by an admin")
	ErrUnknownPlace = errors.New("location not found")
	ErrHostRequired = errors.New("send the host the storefront is serving as X-Site-Host")
)

// Repository persists keys, hosts and settings (repositories.SiteRepository).
type Repository interface {
	CreateKey(ctx context.Context, name, hash, prefix string) (site.PlatformKey, error)
	ListKeys(ctx context.Context) ([]site.PlatformKey, error)
	RevokeKey(ctx context.Context, id uuid.UUID) (site.PlatformKey, error)
	AuthenticateKey(ctx context.Context, hash string) (site.PlatformKey, error)
	TouchKey(ctx context.Context, id uuid.UUID) error
	AddDomain(ctx context.Context, d site.Domain) (site.Domain, error)
	Domains(ctx context.Context, locationID uuid.UUID) ([]site.Domain, error)
	Domain(ctx context.Context, host string) (site.Domain, error)
	RemoveDomain(ctx context.Context, locationID uuid.UUID, host string) (bool, error)
	SetPrimary(ctx context.Context, locationID uuid.UUID, host string) (bool, error)
	SetVerified(ctx context.Context, host string, verified bool) (site.Domain, error)
	Settings(ctx context.Context, locationID uuid.UUID) (site.Settings, error)
}

// LocationReader resolves a location's tenant (the tenancy service).
type LocationReader interface {
	GetLocation(ctx context.Context, id uuid.UUID) (*tenancy.Location, error)
}

// WebChannels makes sure a store has its built-in web channel (the channel
// service), so an admin can set its payments as soon as it has a host.
type WebChannels interface {
	EnsureBuiltinWeb(ctx context.Context, tenantID, locationID uuid.UUID) error
}

type Service struct {
	repo       Repository
	locs       LocationReader
	web        WebChannels
	baseDomain string
	// previewOrigin turns a host into the preview's origin ("" = https://{host}).
	previewOrigin string
	onPublished   []func(ctx context.Context, tenantID, locationID uuid.UUID)
	logger        zerolog.Logger
}

// NewService: baseDomain is the platform's own domain ("" = quiccpos.com).
func NewService(repo Repository, locs LocationReader, baseDomain string, lg zerolog.Logger) *Service {
	if baseDomain = site.NormalizeHost(baseDomain); baseDomain == "" {
		baseDomain = site.DefaultBaseDomain
	}
	return &Service{repo: repo, locs: locs, baseDomain: baseDomain, logger: lg.With().Str("module", "site-service").Logger()}
}

// SetWebChannels plugs in the channel service (set once at startup).
func (s *Service) SetWebChannels(w WebChannels) { s.web = w }

// BaseDomain is the platform's own domain.
func (s *Service) BaseDomain() string { return s.baseDomain }

// --- platform keys (admin) ---------------------------------------------------------

// CreateKey issues a platform key, returned once (only the hash is kept).
func (s *Service) CreateKey(ctx context.Context, name string) (site.PlatformKey, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return site.PlatformKey{}, "", ErrNameRequired
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return site.PlatformKey{}, "", err
	}
	key := site.KeyPrefix + hex.EncodeToString(buf)
	k, err := s.repo.CreateKey(ctx, name, authSvc.HashKey(key), key[:len(site.KeyPrefix)+8])
	if err != nil {
		return site.PlatformKey{}, "", err
	}
	s.logger.Info().Str("platform_key_id", k.ID.String()).Msg("platform key created")
	return k, key, nil
}

func (s *Service) ListKeys(ctx context.Context) ([]site.PlatformKey, error) {
	return s.repo.ListKeys(ctx)
}

// RevokeKey stops a platform key at once (idempotent).
func (s *Service) RevokeKey(ctx context.Context, id uuid.UUID) (site.PlatformKey, error) {
	k, err := s.repo.RevokeKey(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return k, ErrNotFound
	}
	if err == nil {
		s.logger.Info().Str("platform_key_id", id.String()).Msg("platform key revoked")
	}
	return k, err
}

// --- the storefront's sign-in -------------------------------------------------------

// Authenticate checks a platform key (no caching: a revoke takes effect on
// the next request).
func (s *Service) Authenticate(ctx context.Context, key string) (site.PlatformKey, error) {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, site.KeyPrefix) {
		return site.PlatformKey{}, ErrInvalidKey
	}
	k, err := s.repo.AuthenticateKey(ctx, authSvc.HashKey(key))
	if errors.Is(err, pgx.ErrNoRows) {
		return k, ErrInvalidKey
	}
	if err != nil {
		return k, err
	}
	touches.Do(k.ID, func(ctx context.Context) error { return s.repo.TouchKey(ctx, k.ID) })
	return k, nil
}

// Resolve is the store a verified host serves (ErrUnknownSite = none).
func (s *Service) Resolve(ctx context.Context, host string) (site.Domain, error) {
	h := site.NormalizeHost(host)
	if h == "" {
		return site.Domain{}, ErrUnknownSite
	}
	d, err := s.repo.Domain(ctx, h)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !d.Verified) {
		return site.Domain{}, ErrUnknownSite
	}
	return d, err
}

// --- hosts ------------------------------------------------------------------------

// Domains lists a store's hosts, primary first.
func (s *Service) Domains(ctx context.Context, locationID uuid.UUID) ([]site.Domain, error) {
	return s.repo.Domains(ctx, locationID)
}

// AddDomain adds a host to a store. Only an admin may add a host on the
// platform's own domain (verified at once); a store's own domain starts
// unverified. The store's first host becomes its primary.
func (s *Service) AddDomain(ctx context.Context, locationID uuid.UUID, host string, primary, admin bool) (site.Domain, error) {
	h := site.NormalizeHost(host)
	if h == "" {
		return site.Domain{}, site.ErrBadHost
	}
	onPlatform := site.OnPlatform(h, s.baseDomain)
	if onPlatform && !admin {
		return site.Domain{}, ErrPlatformHost
	}
	loc, err := s.locs.GetLocation(ctx, locationID)
	if err != nil {
		return site.Domain{}, ErrUnknownPlace
	}
	cur, err := s.repo.Domains(ctx, locationID)
	if err != nil {
		return site.Domain{}, err
	}
	d, err := s.repo.AddDomain(ctx, site.Domain{Host: h, TenantID: loc.TenantID, LocationID: loc.ID,
		Primary: primary || len(cur) == 0, Verified: onPlatform})
	if err != nil {
		return d, err
	}
	if s.web != nil {
		if err := s.web.EnsureBuiltinWeb(ctx, loc.TenantID, loc.ID); err != nil {
			s.logger.Warn().Err(err).Str("location_id", loc.ID.String()).Msg("web channel not created")
		}
	}
	s.logger.Info().Str("host", h).Str("location_id", loc.ID.String()).Bool("verified", d.Verified).Msg("site host added")
	return d, nil
}

// RemoveDomain deletes one of a store's hosts.
func (s *Service) RemoveDomain(ctx context.Context, locationID uuid.UUID, host string) error {
	ok, err := s.repo.RemoveDomain(ctx, locationID, site.NormalizeHost(host))
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// SetPrimary makes one of a store's hosts its primary (the one links use).
func (s *Service) SetPrimary(ctx context.Context, locationID uuid.UUID, host string) error {
	ok, err := s.repo.SetPrimary(ctx, locationID, site.NormalizeHost(host))
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// SetVerified marks a host verified (admin; a DNS check comes later).
func (s *Service) SetVerified(ctx context.Context, host string, verified bool) (site.Domain, error) {
	d, err := s.repo.SetVerified(ctx, site.NormalizeHost(host), verified)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// --- what the storefront reads -------------------------------------------------------

// View is GET /channel/v1/site: the store's published look and texts, its
// verified hosts and the version (the ETag).
type View struct {
	Version     int64    `json:"version"`
	PrimaryHost string   `json:"primary_host"`
	Hosts       []string `json:"hosts"`
	Settings    jsonRaw  `json:"settings"`
	// Preview: these are the draft's settings (asked with a preview token).
	Preview bool `json:"preview,omitempty"`
	// Email is the store's contact email (receipt replies; the privacy page).
	Email string `json:"email"`
}

type jsonRaw []byte

func (j jsonRaw) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("{}"), nil
	}
	return j, nil
}

// Site is what the storefront renders a store with.
func (s *Service) Site(ctx context.Context, locationID uuid.UUID) (View, error) {
	st, err := s.repo.Settings(ctx, locationID)
	if err != nil {
		return View{}, err
	}
	ds, err := s.repo.Domains(ctx, locationID)
	if err != nil {
		return View{}, err
	}
	v := View{Version: st.Version, Hosts: []string{}, Settings: jsonRaw(st.Published), Email: st.ReplyEmail}
	for _, d := range ds {
		if !d.Verified {
			continue
		}
		v.Hosts = append(v.Hosts, d.Host)
		if d.Primary {
			v.PrimaryHost = d.Host
		}
	}
	return v, nil
}

// PrimaryHost is the store's primary verified host ("" = none).
func (s *Service) PrimaryHost(ctx context.Context, locationID uuid.UUID) string {
	v, err := s.Site(ctx, locationID)
	if err != nil {
		return ""
	}
	return v.PrimaryHost
}
