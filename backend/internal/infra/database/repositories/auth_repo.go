package repositories

import (
	"context"
	"fmt"

	domainAuth "quiccpos/main/internal/domain/auth"
	"quiccpos/main/internal/infra/database/models"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

var _ domainAuth.AuthKeyRepository = (*AuthKeyRepository)(nil)

type AuthKeyRepository struct {
	pool   *pgxpool.Pool
	logger zerolog.Logger
}

func NewAuthKeyRepository(pool *pgxpool.Pool, lg zerolog.Logger) *AuthKeyRepository {
	return &AuthKeyRepository{
		pool:   pool,
		logger: lg.With().Str("module", "auth-key-repo").Logger(),
	}
}

func (r *AuthKeyRepository) Get(ctx context.Context) (string, error) {
	q := models.New(r.pool)
	key, err := q.GetAuthKey(ctx)
	if err != nil {
		return "", fmt.Errorf("get auth key: %w", err)
	}
	return key, nil
}

func (r *AuthKeyRepository) Set(ctx context.Context, key string) error {
	q := models.New(r.pool)
	if err := q.SetAuthKey(ctx, key); err != nil {
		return fmt.Errorf("set auth key: %w", err)
	}
	return nil
}
