package channel

import (
	"context"

	"github.com/google/uuid"
)

// NewChannel is what Repository.Create stores (keyHash "" = no key).
type NewChannel struct {
	TenantID, LocationID uuid.UUID
	Kind                 Kind
	Name                 string
	KeyHash, KeyPrefix   string
	Permissions          Permissions
	Builtin              bool
}

// Repository persists channels. Get/Authenticate return pgx.ErrNoRows for an
// unknown id / key.
type Repository interface {
	Create(ctx context.Context, c NewChannel) (*Channel, error)
	Get(ctx context.Context, id uuid.UUID) (*Channel, error)
	List(ctx context.Context, locationID *uuid.UUID) ([]Channel, error)
	Update(ctx context.Context, id uuid.UUID, name string, p Permissions) (*Channel, error)
	SetKey(ctx context.Context, id uuid.UUID, keyHash, keyPrefix string) (*Channel, error)
	Revoke(ctx context.Context, id uuid.UUID) (*Channel, error)
	Delete(ctx context.Context, id uuid.UUID) (bool, error)
	Authenticate(ctx context.Context, keyHash string) (*Channel, error)
	Touch(ctx context.Context, id uuid.UUID) error
}
