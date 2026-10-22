package tenancy

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrStoreIDTaken means another tenant already holds that vendor store_id
// (tenants.store_id is unique when present).
var ErrStoreIDTaken = errors.New("store_id is already assigned to another tenant")

// ErrUsernameTaken means another tenant already holds that store-manager login
// (tenants.username is unique when present).
var ErrUsernameTaken = errors.New("username already taken")

// ErrPrinterNameTaken: the location already has a printer with that name.
var ErrPrinterNameTaken = errors.New("a printer with that name already exists")

// ErrPrinterIPChanged: the printer no longer has the IP the caller saw (MovePrinterIP).
var ErrPrinterIPChanged = errors.New("printer ip changed since it was read")

// CreateTenantParams creates a tenant and its (only) location. PasswordHash is
// already hashed by the service.
type CreateTenantParams struct {
	Slug, Name, Username, PasswordHash string
	StoreID                            *int64
	LocationSlug, Timezone             string
}

// CreateDeviceParams is what the service hands the repo on mint. KeyHash is
// the SHA-256 of the cleartext key; the cleartext never crosses this
// boundary so the repo cannot accidentally log it.
type CreateDeviceParams struct {
	TenantID   uuid.UUID
	LocationID uuid.UUID
	Name       string
	Kind       string
	KeyHash    string
	KeyPrefix  string
}

// UpdateLocationParams bundles every editable field of a Location. There is
// no partial-update semantics — pass current values for unchanged fields.
type UpdateLocationParams struct {
	ID             uuid.UUID
	Name           string
	Timezone       string
	DailyResetHour int
	Phone          string
	AddressLine1   string
	AddressLine2   string
	Config         Config
}

// TenantAuth is the minimal login lookup result: the tenant id and its stored
// password hash. Empty PasswordHash means the tenant has no credentials set.
type TenantAuth struct {
	TenantID     uuid.UUID
	PasswordHash string
}

type Repository interface {
	// CreateTenant persists a tenant together with its single location (one
	// tenant = one restaurant = one location), in one transaction.
	// Username/PasswordHash are optional ("" = NULL, a tenant that can't log
	// in); StoreID is optional (nil = NULL) — a taken one is ErrStoreIDTaken,
	// a taken username ErrUsernameTaken.
	CreateTenant(ctx context.Context, p CreateTenantParams) (*Tenant, *Location, error)
	GetTenantByID(ctx context.Context, id uuid.UUID) (*Tenant, error)
	// GetTenantByStoreID resolves the vendor store_id to its tenant. Returns
	// pgx.ErrNoRows (wrapped) when no tenant carries that store_id.
	GetTenantByStoreID(ctx context.Context, storeID int64) (*Tenant, error)
	// SetTenantStoreID sets (or clears, with nil) a tenant's vendor store_id.
	// A store_id already held by another tenant returns ErrStoreIDTaken.
	SetTenantStoreID(ctx context.Context, id uuid.UUID, storeID *int64) (*Tenant, error)
	// GetTenantAuthByUsername resolves a store-manager username to its tenant id
	// + password hash for login. Returns pgx.ErrNoRows if the username is unknown.
	GetTenantAuthByUsername(ctx context.Context, username string) (*TenantAuth, error)
	// SetTenantCredentials sets/rotates a tenant's login (passwordHash already
	// hashed). A username held by another tenant returns ErrUsernameTaken.
	SetTenantCredentials(ctx context.Context, id uuid.UUID, username, passwordHash string) error
	ListTenants(ctx context.Context) ([]Tenant, error)
	UpdateTenantConfig(ctx context.Context, id uuid.UUID, cfg Config) (*Tenant, error)

	// DeleteTenantCascade removes the tenant and every row that referenced
	// it: outbox events, orders (+ their child rows via CASCADE), printers,
	// devices, locations, then the tenant itself. All in one transaction.
	// Used by the admin endpoint + the project-level test suite's teardown.
	DeleteTenantCascade(ctx context.Context, id uuid.UUID) error

	GetLocationByID(ctx context.Context, id uuid.UUID) (*Location, error)
	ListLocationsByTenant(ctx context.Context, tenantID uuid.UUID) ([]Location, error)
	ListAllLocations(ctx context.Context) ([]Location, error)
	UpdateLocation(ctx context.Context, p UpdateLocationParams) (*Location, error)
	// SetLocationPin places the store pin by hand (geo_source manual).
	SetLocationPin(ctx context.Context, id uuid.UUID, lat, lng float64) (*Location, error)
	// SetLocationGeocoded stores a geocoded store position unless the pin was
	// placed by hand; false when a manual pin kept it.
	SetLocationGeocoded(ctx context.Context, id uuid.UUID, lat, lng float64) (bool, error)
	// SetLocationMap sets (or, with URL "", clears) the published map file.
	SetLocationMap(ctx context.Context, id uuid.UUID, m LocationMap) (*Location, error)
	// ListLocationsNeedingGeocode: an address but no position (startup backfill).
	ListLocationsNeedingGeocode(ctx context.Context) ([]Location, error)

	CreateDevice(ctx context.Context, p CreateDeviceParams) (*Device, error)
	ListDevicesByLocation(ctx context.Context, locationID uuid.UUID) ([]Device, error)
	ListAllDevices(ctx context.Context) ([]Device, error)
	RevokeDevice(ctx context.Context, id uuid.UUID) error
	// DeleteDevice hard-deletes a device row (vs RevokeDevice, which tombstones it).
	DeleteDevice(ctx context.Context, id uuid.UUID) error

	CreatePrinter(ctx context.Context, tenantID, locationID uuid.UUID, name, ip, mac string) (*Printer, error)
	GetPrinterByID(ctx context.Context, id uuid.UUID) (*Printer, error)
	ListPrintersByLocation(ctx context.Context, locationID uuid.UUID) ([]Printer, error)
	UpdatePrinter(ctx context.Context, id uuid.UUID, name, ip string) (*Printer, error)
	DeletePrinter(ctx context.Context, id uuid.UUID) error
	// SetReceiptPrinter makes printerID the location's only receipt printer
	// (isReceipt=true), or unflags it (false) — one transaction.
	SetReceiptPrinter(ctx context.Context, locationID, printerID uuid.UUID, isReceipt bool) (*Printer, error)
	// DeletePrintersByLocation removes every printer for a location.
	DeletePrintersByLocation(ctx context.Context, locationID uuid.UUID) error
	// MovePrinterIP moves a printer of locationID from oldIP to newIP (and sets
	// its MAC unless mac is empty), logging the change — one transaction.
	// ErrPrinterIPChanged when the printer's IP is no longer oldIP.
	MovePrinterIP(ctx context.Context, locationID, printerID uuid.UUID, oldIP, newIP, mac, source string) (*Printer, error)
	// SetPrinterMAC records a printer's hardware address.
	SetPrinterMAC(ctx context.Context, printerID uuid.UUID, mac string) error
	// InsertPrinterIPChange logs one printer IP change.
	InsertPrinterIPChange(ctx context.Context, c PrinterIPChange) error
	// ListPrinterIPChanges returns a location's printer IP changes, newest first.
	ListPrinterIPChanges(ctx context.Context, locationID uuid.UUID, limit int) ([]PrinterIPChange, error)
}
