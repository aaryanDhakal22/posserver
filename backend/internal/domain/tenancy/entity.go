// Package tenancy is the admin/store-manager domain: tenants, locations,
// the API-key-holding "devices" that authenticate against them, and the
// printers attached to a location. The auth read-path
// (devices.GetActiveByKeyHash) lives in domain/auth — kept separate so the
// blast radius of "admin can mutate everything" is smaller than the blast
// radius of "auth middleware runs on every request."
package tenancy

import (
	"time"

	"github.com/google/uuid"
)

// Config is the free-form jsonb blob attached to tenants and locations. We
// keep it as a typed alias of map[string]any so the repo + service can pass
// it around without going through []byte at every boundary; serialisation
// happens once at the repo edge.
type Config = map[string]any

type Tenant struct {
	ID       uuid.UUID
	Slug     string
	Name     string
	Config   Config
	Username string // store-manager login username; "" = no login set (browser managers use /store/v1/session)
	// StoreID is the external vendor's numeric store id (e.g. 3282). nil = not
	// on the vendor; the online caster then falls back to the store_name match.
	StoreID   *int64
	CreatedAt time.Time
}

type Location struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	Slug           string
	Name           string
	Timezone       string
	DailyResetHour int
	Phone          string
	AddressLine1   string
	AddressLine2   string
	Config         Config
	CreatedAt      time.Time
}

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

// Printer is a thermal printer attached to a location. Used today only for
// receipt dispatch; a future routing layer will pick which printer fires for
// which order based on item categories.
type Printer struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	LocationID uuid.UUID
	Name       string
	IP         string
	// IsReceipt marks the location's one receipt printer (full customer
	// receipt); every other printer is a kitchen station.
	IsReceipt bool
	CreatedAt time.Time
	UpdatedAt time.Time
}
