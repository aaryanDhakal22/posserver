// Package channelApp is the use-case layer for sales channels (main 3.0):
// admin management of a location's channels and their keys, and resolving a
// /channel/v1 bearer key to its channel.
package channelApp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	authSvc "quiccpos/main/internal/app/auth"
	channelDom "quiccpos/main/internal/domain/channel"
	"quiccpos/main/internal/domain/tenancy"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// KeyPrefix starts every channel key.
const KeyPrefix = "qch_"

var (
	ErrNotFound     = errors.New("channel not found")
	ErrInvalidKey   = errors.New("invalid or revoked channel key")
	ErrBuiltin      = errors.New("a built-in channel has no key and can't be revoked or deleted")
	ErrBadKind      = errors.New("kind must be one of: web, operator, counter, vendor")
	ErrNameRequired = errors.New("name is required")
	ErrBadPayment   = errors.New("payments must be card and/or cash")
	ErrUnknownPlace = errors.New("location not found")
)

// LocationReader resolves a location's tenant (the tenancy service).
type LocationReader interface {
	GetLocation(ctx context.Context, id uuid.UUID) (*tenancy.Location, error)
}

type Service struct {
	repo   channelDom.Repository
	locs   LocationReader
	logger zerolog.Logger
}

func NewService(repo channelDom.Repository, locs LocationReader, lg zerolog.Logger) *Service {
	return &Service{repo: repo, locs: locs, logger: lg.With().Str("module", "channel-service").Logger()}
}

// Create adds a channel to a location and returns it with its key, shown
// once (only the hash is stored). nil permissions = the kind's defaults.
func (s *Service) Create(ctx context.Context, locationID uuid.UUID, kind channelDom.Kind, name string,
	perms *channelDom.Permissions) (*channelDom.Channel, string, error) {
	if !kind.Valid() {
		return nil, "", ErrBadKind
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", ErrNameRequired
	}
	p := channelDom.DefaultPermissions(kind)
	if perms != nil {
		if err := validPermissions(*perms); err != nil {
			return nil, "", err
		}
		p = *perms
	}
	loc, err := s.locs.GetLocation(ctx, locationID)
	if err != nil {
		return nil, "", ErrUnknownPlace
	}
	key, err := newKey()
	if err != nil {
		return nil, "", err
	}
	c, err := s.repo.Create(ctx, channelDom.NewChannel{TenantID: loc.TenantID, LocationID: loc.ID, Kind: kind, Name: name,
		KeyHash: authSvc.HashKey(key), KeyPrefix: key[:len(KeyPrefix)+8], Permissions: p})
	if err != nil {
		return nil, "", err
	}
	s.logger.Info().Str("channel_id", c.ID.String()).Str("kind", string(kind)).Str("location_id", loc.ID.String()).Msg("channel created")
	return c, key, nil
}

func (s *Service) List(ctx context.Context, locationID *uuid.UUID) ([]channelDom.Channel, error) {
	return s.repo.List(ctx, locationID)
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*channelDom.Channel, error) {
	c, err := s.repo.Get(ctx, id)
	return c, notFound(err)
}

// Update renames a channel and/or replaces its permissions (nil = keep).
func (s *Service) Update(ctx context.Context, id uuid.UUID, name *string, perms *channelDom.Permissions) (*channelDom.Channel, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	n, p := cur.Name, cur.Permissions
	if name != nil {
		if n = strings.TrimSpace(*name); n == "" {
			return nil, ErrNameRequired
		}
	}
	if perms != nil {
		if err := validPermissions(*perms); err != nil {
			return nil, err
		}
		p = *perms
	}
	c, err := s.repo.Update(ctx, id, n, p)
	return c, notFound(err)
}

// Rotate issues a new key (the old one stops working at once; a revoked
// channel is active again).
func (s *Service) Rotate(ctx context.Context, id uuid.UUID) (*channelDom.Channel, string, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if cur.Builtin {
		return nil, "", ErrBuiltin
	}
	key, err := newKey()
	if err != nil {
		return nil, "", err
	}
	c, err := s.repo.SetKey(ctx, id, authSvc.HashKey(key), key[:len(KeyPrefix)+8])
	if err != nil {
		return nil, "", notFound(err)
	}
	s.logger.Info().Str("channel_id", id.String()).Msg("channel key rotated")
	return c, key, nil
}

// Revoke stops the channel's key (idempotent).
func (s *Service) Revoke(ctx context.Context, id uuid.UUID) (*channelDom.Channel, error) {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if cur.Builtin {
		return nil, ErrBuiltin
	}
	c, err := s.repo.Revoke(ctx, id)
	if err == nil {
		s.logger.Info().Str("channel_id", id.String()).Msg("channel revoked")
	}
	return c, notFound(err)
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	cur, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if cur.Builtin {
		return ErrBuiltin
	}
	ok, err := s.repo.Delete(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// Authenticate resolves a bearer key to its active channel. No caching:
// a revoke or a rotation takes effect on the next request.
func (s *Service) Authenticate(ctx context.Context, key string) (*channelDom.Channel, error) {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, KeyPrefix) {
		return nil, ErrInvalidKey
	}
	c, err := s.repo.Authenticate(ctx, authSvc.HashKey(key))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidKey
	}
	if err != nil {
		return nil, fmt.Errorf("channel lookup: %w", err)
	}
	go func(id uuid.UUID) { _ = s.repo.Touch(context.Background(), id) }(c.ID)
	return c, nil
}

func validPermissions(p channelDom.Permissions) error {
	for _, m := range p.Payments {
		if m != channelDom.PayCard && m != channelDom.PayCash {
			return ErrBadPayment
		}
	}
	if p.Payments == nil || slices.Contains(p.Payments, "") {
		return ErrBadPayment
	}
	return nil
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func newKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return KeyPrefix + hex.EncodeToString(buf), nil
}
