// Package tenancy is the admin/store-manager domain: tenants, locations,
// the API-key-holding "devices" that authenticate against them, and the
// printers attached to a location. The auth read-path
// (devices.GetActiveByKeyHash) lives in domain/auth — kept separate so the
// blast radius of "admin can mutate everything" is smaller than the blast
// radius of "auth middleware runs on every request."
package tenancy

import (
	"errors"
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
	// TestStore: a throwaway test store, deletable with its money records
	// (00043). RetiredAt: retired instead of deleted (devices revoked, no
	// order routed to it); nil = in use.
	TestStore bool
	RetiredAt *time.Time
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

	// Store position (2.0.9). nil until geocoded from the address or pinned
	// by hand; GeoSource is "" | "geocoded" | "manual" (a manual pin is never
	// moved by a geocode).
	Lat, Lng  *float64
	GeoSource string
	// DeliveryRadiusM: addresses farther than this (straight line from the
	// store) are tagged "outside delivery area". Default 8047 (5 mi).
	DeliveryRadiusM int
	// The published offline map file the Pi downloads (MapURL "" = none).
	// MapBBox is [minLng, minLat, maxLng, maxLat].
	MapURL, MapSHA256, MapVersion string
	MapBBox                       []float64
}

// Geo sources for Location.GeoSource.
const (
	GeoSourceGeocoded = "geocoded"
	GeoSourceManual   = "manual"
)

// LocationMap is the published offline map file of a location.
type LocationMap struct {
	URL, SHA256, Version string
	BBox                 []float64
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
	// MAC is the printer's hardware address as the Pi learned it (empty = not
	// known yet) — how the Pi recognises the printer at a new IP.
	MAC string
	// PrintColumns is how many font-A characters fit on the printer's line:
	// 48 (576 dots, 80 mm), 42 (512 dots: an 80 mm roll on a head with a
	// narrower print area) or 32 (384 dots, 58 mm). The Pi lays every slip out
	// for it (agent contract "printers").
	PrintColumns int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Printer print widths (printers.print_columns).
const (
	PrintColumns80 = 48 // 576 dots: the default
	PrintColumns72 = 42 // 512 dots
	PrintColumns58 = 32 // 384 dots
)

// ValidPrintColumns: a width the Pi can lay slips out for.
func ValidPrintColumns(n int) bool {
	return n == PrintColumns80 || n == PrintColumns72 || n == PrintColumns58
}

// ErrBadPrintColumns: a print width other than 48, 42 or 32.
var ErrBadPrintColumns = errors.New("print_columns must be 48, 42 or 32")

// PrinterIPChange is one printer IP change: the Pi following a printer that
// moved (source "agent") or a manager edit (source "manager").
type PrinterIPChange struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	LocationID  uuid.UUID
	PrinterID   uuid.UUID
	PrinterName string
	OldIP       string
	NewIP       string
	MAC         string
	Source      string
	At          time.Time
}

// Printer IP change sources.
const (
	IPChangeByAgent   = "agent"
	IPChangeByManager = "manager"
)
