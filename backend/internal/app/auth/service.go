package authSvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	domainAuth "quiccpos/main/internal/domain/auth"

	"github.com/rs/zerolog"
)

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

func (s *Service) Authenticate(ctx context.Context, rawKey string) (*domainAuth.Device, error) {
	hash := HashKey(rawKey)
	dev, err := s.repo.GetActiveByKeyHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	// Fire-and-forget: last_used_at is a UX nicety, not a correctness signal.
	// Detached context so request cancellation doesn't kill the touch.
	id := dev.ID
	go func() {
		if err := s.repo.Touch(context.Background(), id); err != nil {
			s.logger.Debug().Err(err).Str("device_id", id.String()).Msg("touch failed")
		}
	}()
	return dev, nil
}
