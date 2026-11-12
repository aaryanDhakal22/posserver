package authSvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	domainAuth "quiccpos/main/internal/domain/auth"
	"quiccpos/main/internal/shared/touch"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// touches bounds the last-used stamps (one per id per minute, 5 s each).
var touches = touch.New(touch.Every)

// Service resolves a raw bearer key to a Device by hashing + lookup. Last-used
// timestamp is updated fire-and-forget after a successful lookup; failure to
// update is logged but does not fail auth.
type Service struct {
	repo   domainAuth.Repository
	logger zerolog.Logger
}

func NewService(repo domainAuth.Repository, lg zerolog.Logger) *Service {
	return &Service{
		repo:   repo,
		logger: lg.With().Str("module", "auth-service").Logger(),
	}
}

// HashKey returns the canonical SHA-256 hex digest used as the lookup key.
// Exposed as a package func so the admin/tenancy service uses the same
// hashing on mint as auth uses on lookup — there's exactly one definition of
// "the key" anywhere in the codebase.
func HashKey(rawKey string) string {
	sum := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(sum[:])
}

// KeyState says why a key isn't accepted (F-4): domainAuth.KeyRevoked
// (revoked, or its device / location / store deleted) or KeyUnknown.
func (s *Service) KeyState(ctx context.Context, rawKey string) (string, *uuid.UUID, error) {
	id, revoked, err := s.repo.RevokedKey(ctx, HashKey(rawKey))
	if err != nil {
		return "", nil, err
	}
	if revoked {
		return domainAuth.KeyRevoked, id, nil
	}
	return domainAuth.KeyUnknown, nil, nil
}

func (s *Service) Authenticate(ctx context.Context, rawKey string) (*domainAuth.Device, error) {
	hash := HashKey(rawKey)
	dev, err := s.repo.GetActiveByKeyHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	// Fire-and-forget: last_used_at is a UX nicety, not a correctness signal.
	// Detached context so request cancellation doesn't kill the touch.
	id := dev.ID
	touches.Do(id, func(ctx context.Context) error {
		err := s.repo.Touch(ctx, id)
		if err != nil {
			s.logger.Debug().Err(err).Str("device_id", id.String()).Msg("touch failed")
		}
		return err
	})
	return dev, nil
}
