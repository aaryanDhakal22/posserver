package sse

import "time"

// StoreContext is the "starter pack" main/ emits to an agent on every SSE
// connect, before any order events flow. The agent uses it to:
//
//   - know its own (tenant, location) identity
//   - render receipts (store name, address, phone, tax rate, footer)
//   - reach printers (name + IP)
//   - apply tenant-wide UI prefs (dark_mode, etc.)
//
// The agent persists this payload to SQLite and overwrites it on every
// reconnect — main is the source of truth, but the agent stays operable
// through a brief network blip without re-fetching.
//
// Wire-format conventions:
//   - Empty configs serialize as `{}` (the agent can rely on the key existing).
//   - Time fields use RFC 3339.
//   - PrinterContext.IP is whatever the operator typed in the /store UI; the
//     agent applies it verbatim when opening the ESC/POS TCP socket.
type StoreContext struct {
	Tenant   TenantContext    `json:"tenant"`
	Location LocationContext  `json:"location"`
	Printers []PrinterContext `json:"printers"`
}

type TenantContext struct {
	ID     string         `json:"id"`
	Slug   string         `json:"slug"`
	Name   string         `json:"name"`
	Config map[string]any `json:"config"`
}

type LocationContext struct {
	ID             string         `json:"id"`
	Slug           string         `json:"slug"`
	Name           string         `json:"name"`
	Timezone       string         `json:"timezone"`
	DailyResetHour int            `json:"daily_reset_hour"`
	Phone          string         `json:"phone"`
	AddressLine1   string         `json:"address_line1"`
	AddressLine2   string         `json:"address_line2"`
	Config         map[string]any `json:"config"`
	// Delivery maps (2.0.9): the store pin (null until geocoded / pinned),
	// the "outside delivery area" radius and the offline map file the Pi
	// downloads when its version changes (null = none published).
	Lat             *float64    `json:"lat"`
	Lng             *float64    `json:"lng"`
	DeliveryRadiusM int         `json:"delivery_radius_m"`
	Map             *MapContext `json:"map"`
}

// MapContext is a published PMTiles map file. BBox is [minLng, minLat,
// maxLng, maxLat].
type MapContext struct {
	URL     string    `json:"url"`
	SHA256  string    `json:"sha256"`
	Version string    `json:"version"`
	BBox    []float64 `json:"bbox"`
}

type PrinterContext struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	IP   string `json:"ip"`
	// IsReceipt marks the location's receipt printer (prints the full customer
	// receipt); all others are kitchen stations that print only their items.
	IsReceipt bool `json:"is_receipt"`
	// MAC is the printer's hardware address as the Pi learned it (omitted
	// when not known yet).
	MAC       string    `json:"mac,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}
