// Package operator models call-center order-taker accounts (deliberately NOT
// "agent" — that's the Pi printer bridge). An operator logs in for a revocable
// read-only bearer token that authorizes reading menus for allowed locations.
package operator

import (
	"time"

	"github.com/google/uuid"
)

type Operator struct {
	ID          uuid.UUID `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// AllowedLocation is one (tenant, location) an operator may read.
type AllowedLocation struct {
	TenantID   uuid.UUID `json:"tenant_id"`
	LocationID uuid.UUID `json:"location_id"`
}

// OperatorAuth is the login lookup result (id + stored hash).
type OperatorAuth struct {
	ID           uuid.UUID
	PasswordHash string
}

// TokenAuth is the resolved identity behind a valid bearer token.
type TokenAuth struct {
	TokenID     uuid.UUID
	OperatorID  uuid.UUID
	Username    string
	DisplayName string
}
