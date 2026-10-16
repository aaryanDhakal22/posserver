package operator

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Repository interface {
	CreateOperator(ctx context.Context, username, passwordHash, displayName string) (*Operator, error)
	GetOperator(ctx context.Context, id uuid.UUID) (*Operator, error)
	GetOperatorAuth(ctx context.Context, username string) (*OperatorAuth, error)
	ListOperators(ctx context.Context) ([]Operator, error)
	UpdateOperatorProfile(ctx context.Context, id uuid.UUID, displayName string) (*Operator, error)
	SetOperatorPassword(ctx context.Context, id uuid.UUID, passwordHash string) error
	DeleteOperator(ctx context.Context, id uuid.UUID) error

	// SetAllowedLocations replaces the operator's allowed-location set (tx).
	SetAllowedLocations(ctx context.Context, operatorID uuid.UUID, locs []AllowedLocation) error
	ListAllowedLocations(ctx context.Context, operatorID uuid.UUID) ([]AllowedLocation, error)

	CreateToken(ctx context.Context, operatorID uuid.UUID, tokenHash, prefix string, expiresAt *time.Time) error
	// AuthenticateToken resolves a token hash to its operator, or an error if the
	// token is unknown / revoked / expired.
	AuthenticateToken(ctx context.Context, tokenHash string) (*TokenAuth, error)
	TouchToken(ctx context.Context, tokenID uuid.UUID) error
	RevokeAllTokens(ctx context.Context, operatorID uuid.UUID) error
}
