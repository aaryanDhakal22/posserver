package repositories

import (
	"context"
	"fmt"
	"time"

	"quiccpos/main/internal/domain/operator"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

var _ operator.Repository = (*OperatorRepository)(nil)

type OperatorRepository struct {
	pool   *pgxpool.Pool
	logger zerolog.Logger
}

func NewOperatorRepository(pool *pgxpool.Pool, lg zerolog.Logger) *OperatorRepository {
	return &OperatorRepository{pool: pool, logger: lg.With().Str("module", "operator-repo").Logger()}
}

func (r *OperatorRepository) CreateOperator(ctx context.Context, username, passwordHash, displayName string) (*operator.Operator, error) {
	row, err := models.New(r.pool).CreateOperator(ctx, models.CreateOperatorParams{Username: username, PasswordHash: passwordHash, DisplayName: displayName})
	if err != nil {
		return nil, fmt.Errorf("create operator: %w", err)
	}
	return &operator.Operator{ID: idOf(row.ID), Username: row.Username, DisplayName: row.DisplayName, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}, nil
}

func (r *OperatorRepository) GetOperator(ctx context.Context, id uuid.UUID) (*operator.Operator, error) {
	row, err := models.New(r.pool).GetOperatorByID(ctx, pgID(id))
	if err != nil {
		return nil, err
	}
	return &operator.Operator{ID: idOf(row.ID), Username: row.Username, DisplayName: row.DisplayName, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}, nil
}

func (r *OperatorRepository) GetOperatorAuth(ctx context.Context, username string) (*operator.OperatorAuth, error) {
	row, err := models.New(r.pool).GetOperatorAuthByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	return &operator.OperatorAuth{ID: idOf(row.ID), PasswordHash: row.PasswordHash}, nil
}

func (r *OperatorRepository) ListOperators(ctx context.Context) ([]operator.Operator, error) {
	rows, err := models.New(r.pool).ListOperators(ctx)
	if err != nil {
		return nil, fmt.Errorf("list operators: %w", err)
	}
	out := make([]operator.Operator, 0, len(rows))
	for _, row := range rows {
		out = append(out, operator.Operator{ID: idOf(row.ID), Username: row.Username, DisplayName: row.DisplayName, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time})
	}
	return out, nil
}

func (r *OperatorRepository) UpdateOperatorProfile(ctx context.Context, id uuid.UUID, displayName string) (*operator.Operator, error) {
	row, err := models.New(r.pool).UpdateOperatorProfile(ctx, models.UpdateOperatorProfileParams{ID: pgID(id), DisplayName: displayName})
	if err != nil {
		return nil, fmt.Errorf("update operator: %w", err)
	}
	return &operator.Operator{ID: idOf(row.ID), Username: row.Username, DisplayName: row.DisplayName, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}, nil
}

func (r *OperatorRepository) SetOperatorPassword(ctx context.Context, id uuid.UUID, passwordHash string) error {
	return models.New(r.pool).SetOperatorPassword(ctx, models.SetOperatorPasswordParams{ID: pgID(id), PasswordHash: passwordHash})
}

func (r *OperatorRepository) DeleteOperator(ctx context.Context, id uuid.UUID) error {
	return models.New(r.pool).DeleteOperator(ctx, pgID(id))
}

func (r *OperatorRepository) SetAllowedLocations(ctx context.Context, operatorID uuid.UUID, locs []operator.AllowedLocation) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := models.New(tx)
	if err := q.DeleteOperatorLocations(ctx, pgID(operatorID)); err != nil {
		return fmt.Errorf("clear locations: %w", err)
	}
	for _, l := range locs {
		if err := q.AddOperatorLocation(ctx, models.AddOperatorLocationParams{OperatorID: pgID(operatorID), TenantID: pgID(l.TenantID), LocationID: pgID(l.LocationID)}); err != nil {
			return fmt.Errorf("add location: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func (r *OperatorRepository) ListAllowedLocations(ctx context.Context, operatorID uuid.UUID) ([]operator.AllowedLocation, error) {
	rows, err := models.New(r.pool).ListOperatorLocations(ctx, pgID(operatorID))
	if err != nil {
		return nil, fmt.Errorf("list allowed locations: %w", err)
	}
	out := make([]operator.AllowedLocation, 0, len(rows))
	for _, row := range rows {
		out = append(out, operator.AllowedLocation{TenantID: idOf(row.TenantID), LocationID: idOf(row.LocationID)})
	}
	return out, nil
}

func (r *OperatorRepository) CreateToken(ctx context.Context, operatorID uuid.UUID, tokenHash, prefix string, expiresAt *time.Time) error {
	exp := pgtype.Timestamptz{}
	if expiresAt != nil {
		exp = pgtype.Timestamptz{Time: *expiresAt, Valid: true}
	}
	_, err := models.New(r.pool).CreateOperatorToken(ctx, models.CreateOperatorTokenParams{
		OperatorID: pgID(operatorID), TokenHash: tokenHash, TokenPrefix: prefix, ExpiresAt: exp,
	})
	if err != nil {
		return fmt.Errorf("create token: %w", err)
	}
	return nil
}

func (r *OperatorRepository) AuthenticateToken(ctx context.Context, tokenHash string) (*operator.TokenAuth, error) {
	row, err := models.New(r.pool).GetActiveOperatorTokenByHash(ctx, tokenHash)
	if err != nil {
		return nil, err
	}
	return &operator.TokenAuth{TokenID: idOf(row.TokenID), OperatorID: idOf(row.OperatorID), Username: row.Username, DisplayName: row.DisplayName}, nil
}

func (r *OperatorRepository) TouchToken(ctx context.Context, tokenID uuid.UUID) error {
	return models.New(r.pool).TouchOperatorToken(ctx, pgID(tokenID))
}

func (r *OperatorRepository) RevokeAllTokens(ctx context.Context, operatorID uuid.UUID) error {
	return models.New(r.pool).RevokeAllOperatorTokens(ctx, pgID(operatorID))
}
