package authSvc

import (
	"context"

	domainAuth "quiccpos/main/internal/domain/auth"

	"github.com/rs/zerolog"
)

type Service struct {
	repo   domainAuth.AuthKeyRepository
	logger zerolog.Logger
}

func NewAuthKeyService(repo domainAuth.AuthKeyRepository, lg zerolog.Logger) *Service {
	return &Service{
		repo:   repo,
		logger: lg.With().Str("module", "auth-service").Logger(),
	}
}

func (s *Service) GetKey(ctx context.Context) (string, error) {
	return s.repo.Get(ctx)
}

func (s *Service) SetKey(ctx context.Context, key string) error {
	s.logger.Info().Msg("setting API key")
	return s.repo.Set(ctx, key)
}
