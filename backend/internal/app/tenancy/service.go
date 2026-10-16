// Package tenancyApp is the admin/orchestration layer over the tenancy
// domain. Owns the cleartext-key lifecycle (mint, hash, return-once) so the
// raw bytes only ever exist in memory inside this package for one request.
package tenancyApp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	authSvc "quiccpos/main/internal/app/auth"
	"quiccpos/main/internal/domain/tenancy"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// keyPrefixVisible is how many leading characters of the cleartext key are
// stored as a non-secret identifier in `devices.key_prefix`. Long enough to
// be useful in the UI, short enough that disclosure leaks nothing material.
const keyPrefixVisible = 12

// MintedDevice bundles the persisted Device with the cleartext key that was
// just generated. The cleartext is returned exactly once — to the admin call
// site — and never appears anywhere else, so the operator must copy it
// before navigating away.
type MintedDevice struct {
	Device   tenancy.Device
	ClearKey string
}

type Service struct {
	repo   tenancy.Repository
	logger zerolog.Logger
}

func NewService(repo tenancy.Repository, lg zerolog.Logger) *Service {
	return &Service{
		repo:   repo,
		logger: lg.With().Str("module", "tenancy-service").Logger(),
	}
}

// --- Tenants ---------------------------------------------------------------

// ErrInvalidCredentials is returned by AuthenticateTenant for any login failure
// (unknown username, no password set, or wrong password). Deliberately opaque so
// the caller can't distinguish "no such user" from "wrong password".
var ErrInvalidCredentials = errors.New("invalid username or password")

// ErrNoPrimaryLocation means the tenant has no location yet — an agent can't be
// scoped/minted until one exists. Callers surface this as an actionable error.
var ErrNoPrimaryLocation = errors.New("tenant has no locations")

// defaultTimezone is applied when a location is created/updated without an
// explicit timezone. The restaurants are US-based, so New York over UTC.
const defaultTimezone = "America/New_York"

// CreateTenant creates a restaurant: the tenant and its single location (slug
// "main", same name, default timezone) in one transaction. The location's
// details (address, phone, timezone…) are edited afterwards via UpdateLocation.
func (s *Service) CreateTenant(ctx context.Context, slug, name, username, password string, storeID *int64) (*tenancy.Tenant, *tenancy.Location, error) {
	slug = strings.TrimSpace(slug)
	name = strings.TrimSpace(name)
	username = strings.TrimSpace(username)
	if slug == "" || name == "" {
		return nil, nil, errors.New("slug and name are required")
	}
	// Credentials are optional, but if one is given both must be.
	var passwordHash string
	if username != "" || password != "" {
		if username == "" || password == "" {
			return nil, nil, errors.New("username and password must be provided together")
		}
		h, err := authSvc.HashPassword(password)
		if err != nil {
			return nil, nil, fmt.Errorf("hash password: %w", err)
		}
		passwordHash = h
	}
	if err := validStoreID(storeID); err != nil {
		return nil, nil, err
	}
	return s.repo.CreateTenant(ctx, tenancy.CreateTenantParams{
		Slug: slug, Name: name, Username: username, PasswordHash: passwordHash, StoreID: storeID,
		LocationSlug: defaultLocationSlug, Timezone: defaultTimezone,
	})
}

// defaultLocationSlug is the slug of every tenant's single location.
const defaultLocationSlug = "main"

// ErrInvalidStoreID rejects a non-positive vendor store_id (the vendor's ids
// are positive; 0 is what an absent JSON field decodes to).
var ErrInvalidStoreID = errors.New("store_id must be a positive integer")

func validStoreID(storeID *int64) error {
	if storeID != nil && *storeID <= 0 {
		return ErrInvalidStoreID
	}
	return nil
}

// SetTenantStoreID sets (or clears, with nil) the tenant's external vendor
// store_id — the primary key the online caster routes orders by.
func (s *Service) SetTenantStoreID(ctx context.Context, id uuid.UUID, storeID *int64) (*tenancy.Tenant, error) {
	if id == uuid.Nil {
		return nil, errors.New("tenant id is required")
	}
	if err := validStoreID(storeID); err != nil {
		return nil, err
	}
	return s.repo.SetTenantStoreID(ctx, id, storeID)
}

// SetTenantCredentials sets/rotates a tenant's store-manager login. Hashes the
// password with the same scheme as auth (HashKey). Both fields are required.
func (s *Service) SetTenantCredentials(ctx context.Context, id uuid.UUID, username, password string) error {
	username = strings.TrimSpace(username)
	if id == uuid.Nil {
		return errors.New("tenant id is required")
	}
	if username == "" || password == "" {
		return errors.New("username and password are required")
	}
	h, err := authSvc.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return s.repo.SetTenantCredentials(ctx, id, username, h)
}

// AuthenticateTenant validates a store-manager username/password and returns the
// owning tenant. All failure modes collapse to ErrInvalidCredentials.
func (s *Service) AuthenticateTenant(ctx context.Context, username, password string) (*tenancy.Tenant, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, ErrInvalidCredentials
	}
	auth, err := s.repo.GetTenantAuthByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("lookup tenant auth: %w", err)
	}
	ok, needsRehash := authSvc.VerifyPassword(password, auth.PasswordHash)
	if !ok {
		return nil, ErrInvalidCredentials
	}
	if needsRehash {
		// Legacy SHA-256 hash: upgrade to bcrypt now that we know the password.
		// Best-effort — a failure here must not fail the login.
		if h, err := authSvc.HashPassword(password); err == nil {
			if err := s.repo.SetTenantCredentials(ctx, auth.TenantID, username, h); err != nil {
				s.logger.Warn().Err(err).Str("tenant_id", auth.TenantID.String()).Msg("password rehash to bcrypt failed")
			}
		}
	}
	return s.repo.GetTenantByID(ctx, auth.TenantID)
}

// PrimaryLocation returns the tenant's first location (creation order). Today a
// tenant has exactly one location; this is where multi-location selection would
// hook in later.
func (s *Service) PrimaryLocation(ctx context.Context, tenantID uuid.UUID) (*tenancy.Location, error) {
	locs, err := s.repo.ListLocationsByTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list locations: %w", err)
	}
	if len(locs) == 0 {
		return nil, ErrNoPrimaryLocation
	}
	return &locs[0], nil
}

// ErrStoreUnresolved means an order matched no tenant by store_id, and its
// store_name matched zero — or ambiguously more than one — tenants. The online caster returns it so the SQS consumer DLQs
// the order rather than persisting it to a wrong/placeholder scope.
var ErrStoreUnresolved = errors.New("store_id/store_name did not resolve to a tenant")

// ResolveMethod says which key ResolveStore matched an order on.
type ResolveMethod string

const (
	ResolvedByStoreID      ResolveMethod = "store_id"
	ResolvedByNameFallback ResolveMethod = "name_fallback"
)

// ResolveStore maps a vendor order to its (tenant, location). The vendor's
// numeric store_id is authoritative: when it's set (> 0) and a tenant carries
// it, that tenant wins. Otherwise — store_id absent (0) or not mapped to any
// tenant — it falls back to the fuzzy store_name match (ResolveStoreByName).
// A mapped tenant with no location is ErrStoreUnresolved (no fallback: the id
// matched, the tenant just can't receive orders yet).
func (s *Service) ResolveStore(ctx context.Context, storeID int64, storeName string) (uuid.UUID, uuid.UUID, ResolveMethod, error) {
	if storeID > 0 {
		t, err := s.repo.GetTenantByStoreID(ctx, storeID)
		switch {
		case err == nil:
			loc, lerr := s.PrimaryLocation(ctx, t.ID)
			if lerr != nil {
				return uuid.Nil, uuid.Nil, ResolvedByStoreID, ErrStoreUnresolved
			}
			return t.ID, loc.ID, ResolvedByStoreID, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return uuid.Nil, uuid.Nil, ResolvedByStoreID, fmt.Errorf("resolve store: %w", err)
		}
		// store_id present but unmapped → fall through to the name match.
	}
	tid, lid, err := s.ResolveStoreByName(ctx, storeName)
	return tid, lid, ResolvedByNameFallback, err
}

// ResolveStoreByName maps a vendor store_name to the owning (tenant, location).
//
// TECH-DEBT (fallback only): a fuzzy NAME match — the first 8 lowercased
// characters of store_name must prefix exactly one tenant's name. ResolveStore
// tries the vendor store_id first and only lands here when the order has no
// store_id or it isn't mapped to a tenant yet.
func (s *Service) ResolveStoreByName(ctx context.Context, storeName string) (uuid.UUID, uuid.UUID, error) {
	term := storePrefix(storeName)
	if term == "" {
		return uuid.Nil, uuid.Nil, ErrStoreUnresolved
	}
	tenants, err := s.repo.ListTenants(ctx)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("resolve store: list tenants: %w", err)
	}
	var match *tenancy.Tenant
	for i := range tenants {
		if strings.HasPrefix(strings.ToLower(tenants[i].Name), term) {
			if match != nil {
				return uuid.Nil, uuid.Nil, ErrStoreUnresolved // ambiguous prefix
			}
			match = &tenants[i]
		}
	}
	if match == nil {
		return uuid.Nil, uuid.Nil, ErrStoreUnresolved
	}
	loc, err := s.PrimaryLocation(ctx, match.ID)
	if err != nil {
		return uuid.Nil, uuid.Nil, ErrStoreUnresolved
	}
	return match.ID, loc.ID, nil
}

// storePrefix is the match key for ResolveStoreByName: store_name trimmed,
// lowercased, first 8 bytes. TECH-DEBT alongside the resolver.
func storePrefix(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	if len(s) > 8 {
		s = s[:8]
	}
	return s
}

func (s *Service) ListTenants(ctx context.Context) ([]tenancy.Tenant, error) {
	return s.repo.ListTenants(ctx)
}

func (s *Service) GetTenant(ctx context.Context, id uuid.UUID) (*tenancy.Tenant, error) {
	return s.repo.GetTenantByID(ctx, id)
}

// DeleteTenant cascades through every dependent row.
func (s *Service) DeleteTenant(ctx context.Context, id uuid.UUID) error {
	if id == uuid.Nil {
		return errors.New("tenant id is required")
	}
	return s.repo.DeleteTenantCascade(ctx, id)
}

// --- Locations -------------------------------------------------------------

func (s *Service) ListLocations(ctx context.Context) ([]tenancy.Location, error) {
	return s.repo.ListAllLocations(ctx)
}

func (s *Service) ListLocationsByTenant(ctx context.Context, tenantID uuid.UUID) ([]tenancy.Location, error) {
	return s.repo.ListLocationsByTenant(ctx, tenantID)
}

func (s *Service) GetLocation(ctx context.Context, id uuid.UUID) (*tenancy.Location, error) {
	return s.repo.GetLocationByID(ctx, id)
}

// UpdateLocation rewrites the editable surface in one shot. dailyResetHour
// is bounds-checked (0..23). Empty timezone falls back to defaultTimezone.
func (s *Service) UpdateLocation(ctx context.Context, p tenancy.UpdateLocationParams) (*tenancy.Location, error) {
	if p.ID == uuid.Nil {
		return nil, errors.New("location id is required")
	}
	if strings.TrimSpace(p.Name) == "" {
		return nil, errors.New("name is required")
	}
	if p.Timezone == "" {
		p.Timezone = defaultTimezone
	}
	// The agent uses it (ticket times, the daily driver clock-out) — reject
	// anything the tz database doesn't know rather than push it live.
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return nil, fmt.Errorf("unknown timezone %q", p.Timezone)
	}
	if p.DailyResetHour < 0 || p.DailyResetHour > 23 {
		return nil, errors.New("daily_reset_hour must be 0..23")
	}
	if p.Config == nil {
		p.Config = tenancy.Config{}
	}
	return s.repo.UpdateLocation(ctx, p)
}

// DeleteDevice hard-deletes a device. Prefer RevokeDevice for anything that was
// ever live (keeps an audit tombstone); DeleteDevice is for cleanup of mistakes.
func (s *Service) DeleteDevice(ctx context.Context, id uuid.UUID) error {
	if id == uuid.Nil {
		return errors.New("device id is required")
	}
	return s.repo.DeleteDevice(ctx, id)
}

func (s *Service) UpdateTenantConfig(ctx context.Context, id uuid.UUID, cfg tenancy.Config) (*tenancy.Tenant, error) {
	if id == uuid.Nil {
		return nil, errors.New("tenant id is required")
	}
	if cfg == nil {
		cfg = tenancy.Config{}
	}
	return s.repo.UpdateTenantConfig(ctx, id, cfg)
}

// --- Devices ---------------------------------------------------------------

// Recognised device kinds. The admin surface (/admin/v1/devices) mints
// `agent` only; non-agent kinds (mobile, kiosk, ingest) are minted via the
// store-manager surface (/store/v1/devices). The split is purely on the API
// edge — the devices table itself doesn't care.
const (
	DeviceKindAgent  = "agent"
	DeviceKindMobile = "mobile"
	DeviceKindKiosk  = "kiosk"
	DeviceKindIngest = "ingest"
)

// MintDevice generates a fresh cleartext key, derives its hash, persists the
// device row, and returns both the persisted record and the cleartext. The
// caller MUST surface the cleartext immediately — it is unrecoverable.
//
// Callers from /admin pass kind=agent; callers from /store pass kind=mobile|
// kiosk|ingest. The service does not enforce that split — the handlers do.
func (s *Service) MintDevice(ctx context.Context, locationID uuid.UUID, name, kind string) (*MintedDevice, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("device name is required")
	}
	if kind == "" {
		kind = DeviceKindAgent
	}

	loc, err := s.repo.GetLocationByID(ctx, locationID)
	if err != nil {
		return nil, fmt.Errorf("resolve location: %w", err)
	}

	clear, err := generateKey()
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	hash := authSvc.HashKey(clear)
	prefix := clear[:keyPrefixVisible]

	dev, err := s.repo.CreateDevice(ctx, tenancy.CreateDeviceParams{
		TenantID:   loc.TenantID,
		LocationID: loc.ID,
		Name:       name,
		Kind:       kind,
		KeyHash:    hash,
		KeyPrefix:  prefix,
	})
	if err != nil {
		return nil, fmt.Errorf("persist device: %w", err)
	}
	return &MintedDevice{Device: *dev, ClearKey: clear}, nil
}

func (s *Service) ListDevices(ctx context.Context) ([]tenancy.Device, error) {
	return s.repo.ListAllDevices(ctx)
}

func (s *Service) ListDevicesByLocation(ctx context.Context, locationID uuid.UUID) ([]tenancy.Device, error) {
	return s.repo.ListDevicesByLocation(ctx, locationID)
}

func (s *Service) RevokeDevice(ctx context.Context, id uuid.UUID) error {
	return s.repo.RevokeDevice(ctx, id)
}

// --- Printers --------------------------------------------------------------

func (s *Service) CreatePrinter(ctx context.Context, locationID uuid.UUID, name, ip string) (*tenancy.Printer, error) {
	name = strings.TrimSpace(name)
	ip = strings.TrimSpace(ip)
	if name == "" || ip == "" {
		return nil, errors.New("name and ip are required")
	}
	loc, err := s.repo.GetLocationByID(ctx, locationID)
	if err != nil {
		return nil, fmt.Errorf("resolve location: %w", err)
	}
	p, err := s.repo.CreatePrinter(ctx, loc.TenantID, loc.ID, name, ip)
	if err != nil {
		return nil, err
	}
	// A location always has a receipt printer while it has any printer: the
	// first one added becomes it (the manager can move the flag later).
	if err := s.ensureReceiptPrinter(ctx, loc.ID); err != nil {
		return nil, err
	}
	return s.repo.GetPrinterByID(ctx, p.ID)
}

// SetReceiptPrinter makes printerID the location's receipt printer (the one
// that prints the full customer receipt). Only setting is supported — the
// flag moves, it's never left empty while printers exist.
func (s *Service) SetReceiptPrinter(ctx context.Context, locationID, printerID uuid.UUID) (*tenancy.Printer, error) {
	return s.repo.SetReceiptPrinter(ctx, locationID, printerID, true)
}

// ensureReceiptPrinter flags the location's first printer (by name) as the
// receipt printer when none is flagged — after the first printer is added or
// the receipt printer is deleted.
func (s *Service) ensureReceiptPrinter(ctx context.Context, locationID uuid.UUID) error {
	printers, err := s.repo.ListPrintersByLocation(ctx, locationID)
	if err != nil || len(printers) == 0 {
		return err
	}
	for _, p := range printers {
		if p.IsReceipt {
			return nil
		}
	}
	_, err = s.repo.SetReceiptPrinter(ctx, locationID, printers[0].ID, true)
	return err
}

func (s *Service) ListPrintersByLocation(ctx context.Context, locationID uuid.UUID) ([]tenancy.Printer, error) {
	return s.repo.ListPrintersByLocation(ctx, locationID)
}

func (s *Service) UpdatePrinter(ctx context.Context, id uuid.UUID, name, ip string) (*tenancy.Printer, error) {
	name = strings.TrimSpace(name)
	ip = strings.TrimSpace(ip)
	if name == "" || ip == "" {
		return nil, errors.New("name and ip are required")
	}
	return s.repo.UpdatePrinter(ctx, id, name, ip)
}

func (s *Service) DeletePrinter(ctx context.Context, id uuid.UUID) error {
	p, err := s.repo.GetPrinterByID(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repo.DeletePrinter(ctx, id); err != nil {
		return err
	}
	if p.IsReceipt {
		return s.ensureReceiptPrinter(ctx, p.LocationID) // hand the flag on
	}
	return nil
}

// --- Helpers ---------------------------------------------------------------

// generateKey returns a 256-bit random key as `qpos_<64 hex chars>`. The
// prefix makes leaked keys grep-able in logs and source repos.
func generateKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "qpos_" + hex.EncodeToString(buf), nil
}
