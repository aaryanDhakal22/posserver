// Package auth holds the authentication seam for ingest/SSE callers. A caller
// presents a bearer in the X-STORE-API-KEY header; the middleware SHA-256s
// it, looks up the matching active row in `devices`, and attaches the
// resolved Device to the request context. Tenancy + location flow downstream
// from that Device.
package auth

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Device represents one authenticatable API-key holder: an agent install, the
// mobile app, or any future direct-ingest client. Identity (tenant, location)
// is structural in the row, not encoded in the key, so revoking is just
// flipping revoked_at.
type Device struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	LocationID uuid.UUID
	Name       string
	Kind       string
	KeyPrefix  string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// Repository is what the auth service needs from infra. Lookup-by-hash only:
// admin write operations live behind the admin/tenancy service, not here.
type Repository interface {
	GetActiveByKeyHash(ctx context.Context, hash string) (*Device, error)
	Touch(ctx context.Context, id uuid.UUID) error
	// RevokedKey reports whether a key that isn't active was revoked or
	// deleted (its device, location or store): F-4's device_revoked, vs a
	// key main has never seen (unknown_key).
	RevokedKey(ctx context.Context, hash string) (deviceID *uuid.UUID, revoked bool, err error)
}

// Key states (F-4): what main says about a key it doesn't accept.
const (
	KeyRevoked = "device_revoked" // revoked, or its device / location / store deleted: the Pi may unlink
	KeyUnknown = "unknown_key"    // no record of it (e.g. a main restored from an older backup): the Pi keeps its link
)
