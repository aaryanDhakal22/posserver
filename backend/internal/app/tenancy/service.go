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
	"net"
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
	repo         tenancy.Repository
	logger       zerolog.Logger
	onDeviceGone func(id uuid.UUID)
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
	if err := validUsername(username); err != nil {
		return nil, nil, err
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

// ErrReservedUsername rejects a store login named "admin" (any case): the
// admin's open-as-store session is labelled "admin" in logs, traces and the
// whoami, so a manager account with that name would be indistinguishable.
var ErrReservedUsername = errors.New(`username "admin" is reserved`)

// validUsername checks an already-trimmed username ("" = no login, allowed here).
func validUsername(username string) error {
	if strings.EqualFold(username, "admin") {
		return ErrReservedUsername
	}
	return nil
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
	if err := validUsername(username); err != nil {
		return err
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
	if _, _, err := tenancy.HoursFromConfig(p.Config); err != nil {
		return nil, err
	}
	// The owner's Pushover devices go live to the agent: refuse a name
	// Pushover would refuse, store the list trimmed + de-duplicated.
	if err := tenancy.NormalizeOwnerDevices(p.Config); err != nil {
		return nil, err
	}
	return s.repo.UpdateLocation(ctx, p)
}

// SetLocationPin places the store pin by hand (a manager dragged it to the
// door). Geocoding never moves a manual pin.
func (s *Service) SetLocationPin(ctx context.Context, id uuid.UUID, lat, lng float64) (*tenancy.Location, error) {
	if !validLatLng(lat, lng) {
		return nil, errors.New("lat must be -90..90 and lng -180..180")
	}
	return s.repo.SetLocationPin(ctx, id, lat, lng)
}

// SetLocationGeocoded stores a geocoded store position (no-op on a manual pin).
func (s *Service) SetLocationGeocoded(ctx context.Context, id uuid.UUID, lat, lng float64) (bool, error) {
	return s.repo.SetLocationGeocoded(ctx, id, lat, lng)
}

// SetLocationMap publishes (or clears, with URL "") the location's offline
// map file. A set map needs a sha256, a version and a 4-number bbox.
func (s *Service) SetLocationMap(ctx context.Context, id uuid.UUID, m tenancy.LocationMap) (*tenancy.Location, error) {
	if strings.TrimSpace(m.URL) == "" {
		m = tenancy.LocationMap{}
	} else {
		if !strings.HasPrefix(m.URL, "https://") && !strings.HasPrefix(m.URL, "http://") {
			return nil, errors.New("url must be http(s)")
		}
		if b, err := hex.DecodeString(m.SHA256); err != nil || len(b) != 32 {
			return nil, errors.New("sha256 must be 64 hex characters")
		}
		if strings.TrimSpace(m.Version) == "" {
			return nil, errors.New("version is required")
		}
		if len(m.BBox) != 4 || m.BBox[0] >= m.BBox[2] || m.BBox[1] >= m.BBox[3] ||
			!validLatLng(m.BBox[1], m.BBox[0]) || !validLatLng(m.BBox[3], m.BBox[2]) {
			return nil, errors.New("bbox must be [minLng, minLat, maxLng, maxLat]")
		}
		m.SHA256 = strings.ToLower(m.SHA256)
	}
	return s.repo.SetLocationMap(ctx, id, m)
}

// ListLocationsNeedingGeocode lists stores with an address but no position.
func (s *Service) ListLocationsNeedingGeocode(ctx context.Context) ([]tenancy.Location, error) {
	return s.repo.ListLocationsNeedingGeocode(ctx)
}

func validLatLng(lat, lng float64) bool {
	return lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180 && !(lat == 0 && lng == 0)
}

// DeleteDevice hard-deletes a device. Prefer RevokeDevice for anything that was
// ever live (keeps an audit tombstone); DeleteDevice is for cleanup of mistakes.
func (s *Service) DeleteDevice(ctx context.Context, id uuid.UUID) error {
	if id == uuid.Nil {
		return errors.New("device id is required")
	}
	if err := s.repo.DeleteDevice(ctx, id); err != nil {
		return err
	}
	s.deviceGone(id)
	return nil
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
	if err := s.repo.RevokeDevice(ctx, id); err != nil {
		return err
	}
	s.deviceGone(id)
	return nil
}

// OnDeviceGone registers a hook run after a device is revoked or deleted
// (cmd/api wires the SSE broker's DisconnectDevice, so a revoked agent's live
// stream is cut at once instead of at its next request).
func (s *Service) OnDeviceGone(fn func(id uuid.UUID)) { s.onDeviceGone = fn }

func (s *Service) deviceGone(id uuid.UUID) {
	if s.onDeviceGone != nil {
		s.onDeviceGone(id)
	}
}

// --- Printers --------------------------------------------------------------

func (s *Service) CreatePrinter(ctx context.Context, locationID uuid.UUID, name, ip string) (*tenancy.Printer, error) {
	return s.AddPrinter(ctx, locationID, name, ip, "")
}

// AddPrinter adds a printer with its hardware address when known (the Pi
// found it on the network; empty otherwise). tenancy.ErrPrinterNameTaken when the
// location already has a printer of that name.
func (s *Service) AddPrinter(ctx context.Context, locationID uuid.UUID, name, ip, mac string) (*tenancy.Printer, error) {
	name = strings.TrimSpace(name)
	ip = strings.TrimSpace(ip)
	if name == "" || ip == "" {
		return nil, errors.New("name and ip are required")
	}
	loc, err := s.repo.GetLocationByID(ctx, locationID)
	if err != nil {
		return nil, fmt.Errorf("resolve location: %w", err)
	}
	p, err := s.repo.CreatePrinter(ctx, loc.TenantID, loc.ID, name, ip, NormalizeMAC(mac))
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

// NormalizeMAC returns mac in lowercase colon form ("aa:bb:cc:dd:ee:ff"), or
// empty when it isn't a hardware address.
func NormalizeMAC(mac string) string {
	hw, err := net.ParseMAC(strings.TrimSpace(mac))
	if err != nil {
		return ""
	}
	return hw.String()
}

// MoveResult is the outcome of MovePrinter. Reason is "" when applied, else
// "stale_old_ip" (the printer no longer has the IP the Pi saw — a manager edit
// or an earlier move won) or "not_found" (no such printer at this location).
type MoveResult struct {
	Applied bool
	Reason  string
	// Changed is true when the printer's IP actually moved (store_config needs
	// a push); an applied repeat of a move already made changes nothing.
	Changed bool
	Printer *tenancy.Printer
}

// Move outcomes (the printer.moved reply's reason).
const (
	MoveStaleOldIP = "stale_old_ip"
	MoveNotFound   = "not_found"
)

// MovePrinter follows a printer the Pi found at a new IP: only a printer of
// this (tenant, location), only while it still has oldIP. The change is logged
// (source "agent"). A repeat of a move already made (the printer is at newIP)
// is applied without a second log row.
func (s *Service) MovePrinter(ctx context.Context, tenantID, locationID, printerID uuid.UUID, oldIP, newIP, mac string) (MoveResult, error) {
	oldIP, newIP, mac = strings.TrimSpace(oldIP), strings.TrimSpace(newIP), NormalizeMAC(mac)
	p, err := s.repo.GetPrinterByID(ctx, printerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return MoveResult{Reason: MoveNotFound}, nil
	}
	if err != nil {
		return MoveResult{}, err
	}
	if p.TenantID != tenantID || p.LocationID != locationID {
		return MoveResult{Reason: MoveNotFound}, nil
	}
	if p.IP == newIP {
		if mac != "" && p.MAC != mac {
			if err := s.repo.SetPrinterMAC(ctx, p.ID, mac); err != nil {
				return MoveResult{}, err
			}
			p.MAC = mac
		}
		return MoveResult{Applied: true, Printer: p}, nil
	}
	if p.IP != oldIP {
		return MoveResult{Reason: MoveStaleOldIP, Printer: p}, nil
	}
	moved, err := s.repo.MovePrinterIP(ctx, locationID, printerID, oldIP, newIP, mac, tenancy.IPChangeByAgent)
	if errors.Is(err, tenancy.ErrPrinterIPChanged) {
		return MoveResult{Reason: MoveStaleOldIP, Printer: p}, nil
	}
	if err != nil {
		return MoveResult{}, err
	}
	return MoveResult{Applied: true, Changed: true, Printer: moved}, nil
}

// ListPrinterIPChanges returns the location's printer IP changes, newest
// first (limit clamped to 1..100, default 20).
func (s *Service) ListPrinterIPChanges(ctx context.Context, locationID uuid.UUID, limit int) ([]tenancy.PrinterIPChange, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return s.repo.ListPrinterIPChanges(ctx, locationID, limit)
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

// UpdatePrinter is the manager's edit. A changed IP is logged (source
// "manager") next to the Pi's own moves.
func (s *Service) UpdatePrinter(ctx context.Context, id uuid.UUID, name, ip string) (*tenancy.Printer, error) {
	name = strings.TrimSpace(name)
	ip = strings.TrimSpace(ip)
	if name == "" || ip == "" {
		return nil, errors.New("name and ip are required")
	}
	before, err := s.repo.GetPrinterByID(ctx, id)
	if err != nil {
		return nil, err
	}
	p, err := s.repo.UpdatePrinter(ctx, id, name, ip)
	if err != nil {
		return nil, err
	}
	if before.IP != p.IP {
		if err := s.repo.InsertPrinterIPChange(ctx, tenancy.PrinterIPChange{
			TenantID: p.TenantID, LocationID: p.LocationID, PrinterID: p.ID,
			OldIP: before.IP, NewIP: p.IP, MAC: p.MAC, Source: tenancy.IPChangeByManager,
		}); err != nil {
			// The edit itself stands; only its history line is missing.
			s.logger.Warn().Ctx(ctx).Err(err).Str("printer_id", id.String()).Msg("printer ip change not logged")
		}
	}
	return p, nil
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

// ManagerCredential returns the tenant's manager username and password hash
// (bcrypt). A linked agent caches the hash so its web UI can be unlocked with
// the manager login while the internet is down.
func (s *Service) ManagerCredential(ctx context.Context, tenantID uuid.UUID) (username, hash string, err error) {
	t, err := s.repo.GetTenantByID(ctx, tenantID)
	if err != nil {
		return "", "", err
	}
	if t.Username == "" {
		return "", "", ErrInvalidCredentials
	}
	auth, err := s.repo.GetTenantAuthByUsername(ctx, t.Username)
	if err != nil {
		return "", "", err
	}
	return t.Username, auth.PasswordHash, nil
}
