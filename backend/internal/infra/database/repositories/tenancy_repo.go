package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

var _ tenancy.Repository = (*TenancyRepository)(nil)

type TenancyRepository struct {
	pool   *pgxpool.Pool
	logger zerolog.Logger
}

func NewTenancyRepository(pool *pgxpool.Pool, lg zerolog.Logger) *TenancyRepository {
	return &TenancyRepository{
		pool:   pool,
		logger: lg.With().Str("module", "tenancy-repo").Logger(),
	}
}

// --- Tenants ---------------------------------------------------------------

// CreateTenant creates a tenant AND its single location in one transaction —
// one tenant = one restaurant = one location (migration 00003 enforces it).
func (r *TenancyRepository) CreateTenant(ctx context.Context, p tenancy.CreateTenantParams) (*tenancy.Tenant, *tenancy.Location, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := models.New(tx)
	row, err := q.CreateTenant(ctx, models.CreateTenantParams{
		Slug:         p.Slug,
		Name:         p.Name,
		Username:     pgtype.Text{String: p.Username, Valid: p.Username != ""},
		PasswordHash: pgtype.Text{String: p.PasswordHash, Valid: p.PasswordHash != ""},
		StoreID:      toPgInt8(p.StoreID),
	})
	if err != nil {
		if isStoreIDViolation(err) {
			return nil, nil, tenancy.ErrStoreIDTaken
		}
		if isUsernameViolation(err) {
			return nil, nil, tenancy.ErrUsernameTaken
		}
		return nil, nil, fmt.Errorf("create tenant: %w", err)
	}
	loc, err := q.CreateLocation(ctx, models.CreateLocationParams{
		TenantID:       row.ID,
		Slug:           p.LocationSlug,
		Name:           p.Name,
		Timezone:       p.Timezone,
		DailyResetHour: 0,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create location: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("commit tx: %w", err)
	}
	return toDomainTenant(row), toDomainLocation(loc), nil
}

func (r *TenancyRepository) GetTenantByStoreID(ctx context.Context, storeID int64) (*tenancy.Tenant, error) {
	q := models.New(r.pool)
	row, err := q.GetTenantByStoreID(ctx, pgtype.Int8{Int64: storeID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("get tenant by store_id: %w", err)
	}
	return toDomainTenant(row), nil
}

func (r *TenancyRepository) SetTenantStoreID(ctx context.Context, id uuid.UUID, storeID *int64) (*tenancy.Tenant, error) {
	q := models.New(r.pool)
	row, err := q.SetTenantStoreID(ctx, models.SetTenantStoreIDParams{
		ID:      pgtype.UUID{Bytes: id, Valid: true},
		StoreID: toPgInt8(storeID),
	})
	if err != nil {
		if isStoreIDViolation(err) {
			return nil, tenancy.ErrStoreIDTaken
		}
		return nil, fmt.Errorf("set tenant store_id: %w", err)
	}
	return toDomainTenant(row), nil
}

// isStoreIDViolation narrows a unique violation to the store_id index so a
// slug clash on create still surfaces as its own error.
func isStoreIDViolation(err error) bool {
	return isUniqueViolationOn(err, "idx_tenants_store_id")
}

// isUsernameViolation narrows a unique violation to the store-manager login.
func isUsernameViolation(err error) bool {
	return isUniqueViolationOn(err, "idx_tenants_username")
}

func isUniqueViolationOn(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation &&
		pgErr.ConstraintName == constraint
}

func toPgInt8(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

func (r *TenancyRepository) SetTenantCredentials(ctx context.Context, id uuid.UUID, username, passwordHash string) error {
	q := models.New(r.pool)
	if err := q.SetTenantCredentials(ctx, models.SetTenantCredentialsParams{
		ID:           pgtype.UUID{Bytes: id, Valid: true},
		Username:     pgtype.Text{String: username, Valid: username != ""},
		PasswordHash: pgtype.Text{String: passwordHash, Valid: passwordHash != ""},
	}); err != nil {
		if isUsernameViolation(err) {
			return tenancy.ErrUsernameTaken
		}
		return fmt.Errorf("set tenant credentials: %w", err)
	}
	return nil
}

func (r *TenancyRepository) GetTenantAuthByUsername(ctx context.Context, username string) (*tenancy.TenantAuth, error) {
	q := models.New(r.pool)
	row, err := q.GetTenantAuthByUsername(ctx, pgtype.Text{String: username, Valid: true})
	if err != nil {
		return nil, err // pgx.ErrNoRows bubbles up; the service maps it to bad-credentials
	}
	return &tenancy.TenantAuth{
		TenantID:     uuid.UUID(row.ID.Bytes),
		PasswordHash: row.PasswordHash.String, // "" when NULL
	}, nil
}

func (r *TenancyRepository) GetTenantByID(ctx context.Context, id uuid.UUID) (*tenancy.Tenant, error) {
	q := models.New(r.pool)
	row, err := q.GetTenantByID(ctx, pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("get tenant: %w", err)
	}
	return toDomainTenant(row), nil
}

func (r *TenancyRepository) ListTenants(ctx context.Context) ([]tenancy.Tenant, error) {
	q := models.New(r.pool)
	rows, err := q.ListTenants(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	out := make([]tenancy.Tenant, 0, len(rows))
	for _, r := range rows {
		out = append(out, *toDomainTenant(r))
	}
	return out, nil
}

func (r *TenancyRepository) UpdateTenantConfig(ctx context.Context, id uuid.UUID, cfg tenancy.Config) (*tenancy.Tenant, error) {
	q := models.New(r.pool)
	raw, err := marshalConfig(cfg)
	if err != nil {
		return nil, err
	}
	row, err := q.UpdateTenantConfig(ctx, models.UpdateTenantConfigParams{
		ID:     pgtype.UUID{Bytes: id, Valid: true},
		Config: raw,
	})
	if err != nil {
		return nil, fmt.Errorf("update tenant config: %w", err)
	}
	return toDomainTenant(row), nil
}

// DeleteTenantCascade hard-deletes the tenant and every dependent row.
// Done as an explicit chain inside one tx rather than via DB-side CASCADE
// because the composite FKs (devices/printers/orders → locations) are
// declared RESTRICT — that's intentional safety for normal operation.
// Only this method (and the admin/test path that calls it) bypasses it.
func (r *TenancyRepository) DeleteTenantCascade(ctx context.Context, id uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	q := models.New(tx)
	pid := pgtype.UUID{Bytes: id, Valid: true}

	// Order matters: children before parents, then the tenant itself.
	// `outbox_events` doesn't FK to anything tenant-side but rows reference
	// `tenant_id` for routing; drop them first to keep the dispatcher's
	// scan clean.
	for _, step := range []struct {
		name string
		fn   func() error
	}{
		{"outbox_events", func() error { return q.DeleteTenantOutboxEvents(ctx, pid) }},
		{"orders", func() error { return q.DeleteTenantOrders(ctx, pid) }},
		{"printers", func() error { return q.DeleteTenantPrinters(ctx, pid) }},
		{"devices", func() error { return q.DeleteTenantDevices(ctx, pid) }},
		{"locations", func() error { return q.DeleteTenantLocations(ctx, pid) }},
		{"tenants", func() error { return q.DeleteTenant(ctx, pid) }},
	} {
		if err := step.fn(); err != nil {
			return fmt.Errorf("delete %s: %w", step.name, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	r.logger.Info().Ctx(ctx).Str("tenant_id", id.String()).Msg("tenant cascade-deleted")
	return nil
}

// DeleteDevice hard-deletes a single device row (vs RevokeDevice's tombstone).
func (r *TenancyRepository) DeleteDevice(ctx context.Context, id uuid.UUID) error {
	q := models.New(r.pool)
	if err := q.DeleteDevice(ctx, pgtype.UUID{Bytes: id, Valid: true}); err != nil {
		return fmt.Errorf("delete device: %w", err)
	}
	return nil
}

// --- Locations -------------------------------------------------------------

func (r *TenancyRepository) GetLocationByID(ctx context.Context, id uuid.UUID) (*tenancy.Location, error) {
	q := models.New(r.pool)
	row, err := q.GetLocationByID(ctx, pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("get location: %w", err)
	}
	return toDomainLocation(row), nil
}

func (r *TenancyRepository) ListLocationsByTenant(ctx context.Context, tenantID uuid.UUID) ([]tenancy.Location, error) {
	q := models.New(r.pool)
	rows, err := q.ListLocationsByTenant(ctx, pgtype.UUID{Bytes: tenantID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list locations: %w", err)
	}
	out := make([]tenancy.Location, 0, len(rows))
	for _, row := range rows {
		out = append(out, *toDomainLocation(row))
	}
	return out, nil
}

func (r *TenancyRepository) ListAllLocations(ctx context.Context) ([]tenancy.Location, error) {
	q := models.New(r.pool)
	rows, err := q.ListAllLocations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list all locations: %w", err)
	}
	out := make([]tenancy.Location, 0, len(rows))
	for _, row := range rows {
		out = append(out, *toDomainLocation(row))
	}
	return out, nil
}

// SetLocationPin — see tenancy.Repository.
func (r *TenancyRepository) SetLocationPin(ctx context.Context, id uuid.UUID, lat, lng float64) (*tenancy.Location, error) {
	row, err := models.New(r.pool).SetLocationPin(ctx, models.SetLocationPinParams{
		ID: pgtype.UUID{Bytes: id, Valid: true}, Lat: pgtype.Float8{Float64: lat, Valid: true}, Lng: pgtype.Float8{Float64: lng, Valid: true},
	})
	if err != nil {
		return nil, err
	}
	return toDomainLocation(row), nil
}

// SetLocationGeocoded — see tenancy.Repository.
func (r *TenancyRepository) SetLocationGeocoded(ctx context.Context, id uuid.UUID, lat, lng float64) (bool, error) {
	n, err := models.New(r.pool).SetLocationGeocoded(ctx, models.SetLocationGeocodedParams{
		ID: pgtype.UUID{Bytes: id, Valid: true}, Lat: pgtype.Float8{Float64: lat, Valid: true}, Lng: pgtype.Float8{Float64: lng, Valid: true},
	})
	return n > 0, err
}

// SetLocationMap — see tenancy.Repository.
func (r *TenancyRepository) SetLocationMap(ctx context.Context, id uuid.UUID, m tenancy.LocationMap) (*tenancy.Location, error) {
	bbox := m.BBox
	if bbox == nil {
		bbox = []float64{}
	}
	row, err := models.New(r.pool).SetLocationMap(ctx, models.SetLocationMapParams{
		ID: pgtype.UUID{Bytes: id, Valid: true}, MapUrl: m.URL, MapSha256: m.SHA256, MapVersion: m.Version, MapBbox: bbox,
	})
	if err != nil {
		return nil, err
	}
	return toDomainLocation(row), nil
}

// ListLocationsNeedingGeocode — see tenancy.Repository.
func (r *TenancyRepository) ListLocationsNeedingGeocode(ctx context.Context) ([]tenancy.Location, error) {
	rows, err := models.New(r.pool).ListLocationsNeedingGeocode(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]tenancy.Location, 0, len(rows))
	for _, row := range rows {
		out = append(out, tenancy.Location{ID: uuid.UUID(row.ID.Bytes), TenantID: uuid.UUID(row.TenantID.Bytes)})
	}
	return out, nil
}

func (r *TenancyRepository) UpdateLocation(ctx context.Context, p tenancy.UpdateLocationParams) (*tenancy.Location, error) {
	q := models.New(r.pool)
	raw, err := marshalConfig(p.Config)
	if err != nil {
		return nil, err
	}
	row, err := q.UpdateLocationDetails(ctx, models.UpdateLocationDetailsParams{
		ID:             pgtype.UUID{Bytes: p.ID, Valid: true},
		Name:           p.Name,
		Timezone:       p.Timezone,
		DailyResetHour: int16(p.DailyResetHour),
		Phone:          p.Phone,
		AddressLine1:   p.AddressLine1,
		AddressLine2:   p.AddressLine2,
		Config:         raw,
	})
	if err != nil {
		return nil, fmt.Errorf("update location: %w", err)
	}
	return toDomainLocation(row), nil
}

// --- Devices ---------------------------------------------------------------

func (r *TenancyRepository) CreateDevice(ctx context.Context, p tenancy.CreateDeviceParams) (*tenancy.Device, error) {
	q := models.New(r.pool)
	row, err := q.CreateDevice(ctx, models.CreateDeviceParams{
		TenantID:   pgtype.UUID{Bytes: p.TenantID, Valid: true},
		LocationID: pgtype.UUID{Bytes: p.LocationID, Valid: true},
		Name:       p.Name,
		Kind:       p.Kind,
		KeyHash:    p.KeyHash,
		KeyPrefix:  p.KeyPrefix,
	})
	if err != nil {
		return nil, fmt.Errorf("create device: %w", err)
	}
	return toDomainTenancyDevice(row), nil
}

func (r *TenancyRepository) ListDevicesByLocation(ctx context.Context, locationID uuid.UUID) ([]tenancy.Device, error) {
	q := models.New(r.pool)
	rows, err := q.ListDevicesByLocation(ctx, pgtype.UUID{Bytes: locationID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	out := make([]tenancy.Device, 0, len(rows))
	for _, row := range rows {
		out = append(out, *toDomainTenancyDevice(row))
	}
	return out, nil
}

func (r *TenancyRepository) ListAllDevices(ctx context.Context) ([]tenancy.Device, error) {
	q := models.New(r.pool)
	rows, err := q.ListAllDevices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list all devices: %w", err)
	}
	out := make([]tenancy.Device, 0, len(rows))
	for _, row := range rows {
		out = append(out, *toDomainTenancyDevice(row))
	}
	return out, nil
}

func (r *TenancyRepository) RevokeDevice(ctx context.Context, id uuid.UUID) error {
	q := models.New(r.pool)
	if err := q.RevokeDevice(ctx, pgtype.UUID{Bytes: id, Valid: true}); err != nil {
		return fmt.Errorf("revoke device: %w", err)
	}
	return nil
}

// --- Printers --------------------------------------------------------------

func (r *TenancyRepository) CreatePrinter(ctx context.Context, tenantID, locationID uuid.UUID, name, ip, mac string) (*tenancy.Printer, error) {
	q := models.New(r.pool)
	row, err := q.CreatePrinter(ctx, models.CreatePrinterParams{
		TenantID:   pgtype.UUID{Bytes: tenantID, Valid: true},
		LocationID: pgtype.UUID{Bytes: locationID, Valid: true},
		Name:       name,
		IP:         ip,
		MAC:        mac,
	})
	if err != nil {
		if isUniqueViolationOn(err, printerNameKey) {
			return nil, tenancy.ErrPrinterNameTaken
		}
		return nil, fmt.Errorf("create printer: %w", err)
	}
	return toDomainPrinter(row), nil
}

func (r *TenancyRepository) GetPrinterByID(ctx context.Context, id uuid.UUID) (*tenancy.Printer, error) {
	q := models.New(r.pool)
	row, err := q.GetPrinterByID(ctx, pgtype.UUID{Bytes: id, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("get printer: %w", err)
	}
	return toDomainPrinter(row), nil
}

func (r *TenancyRepository) ListPrintersByLocation(ctx context.Context, locationID uuid.UUID) ([]tenancy.Printer, error) {
	q := models.New(r.pool)
	rows, err := q.ListPrintersByLocation(ctx, pgtype.UUID{Bytes: locationID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list printers: %w", err)
	}
	out := make([]tenancy.Printer, 0, len(rows))
	for _, row := range rows {
		out = append(out, *toDomainPrinter(row))
	}
	return out, nil
}

func (r *TenancyRepository) UpdatePrinter(ctx context.Context, id uuid.UUID, name, ip string) (*tenancy.Printer, error) {
	q := models.New(r.pool)
	row, err := q.UpdatePrinter(ctx, models.UpdatePrinterParams{
		ID:   pgtype.UUID{Bytes: id, Valid: true},
		Name: name,
		IP:   ip,
	})
	if err != nil {
		if isUniqueViolationOn(err, printerNameKey) {
			return nil, tenancy.ErrPrinterNameTaken
		}
		return nil, fmt.Errorf("update printer: %w", err)
	}
	return toDomainPrinter(row), nil
}

// SetReceiptPrinter flags printerID as the location's receipt printer (clearing
// any previous one first, same tx — the partial unique index allows only one)
// or unflags it.
func (r *TenancyRepository) SetReceiptPrinter(ctx context.Context, locationID, printerID uuid.UUID, isReceipt bool) (*tenancy.Printer, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := models.New(tx)
	if isReceipt {
		if err := q.ClearReceiptPrinter(ctx, pgtype.UUID{Bytes: locationID, Valid: true}); err != nil {
			return nil, fmt.Errorf("clear receipt printer: %w", err)
		}
	}
	row, err := q.SetPrinterReceipt(ctx, models.SetPrinterReceiptParams{
		ID:        pgtype.UUID{Bytes: printerID, Valid: true},
		IsReceipt: isReceipt,
	})
	if err != nil {
		return nil, fmt.Errorf("set receipt printer: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}
	return toDomainPrinter(row), nil
}

func (r *TenancyRepository) DeletePrinter(ctx context.Context, id uuid.UUID) error {
	q := models.New(r.pool)
	if err := q.DeletePrinter(ctx, pgtype.UUID{Bytes: id, Valid: true}); err != nil {
		return fmt.Errorf("delete printer: %w", err)
	}
	return nil
}

// printerNameKey is the (location_id, name) unique constraint on printers.
const printerNameKey = "printers_location_id_name_key"

// MovePrinterIP updates the printer's IP (+ MAC) only while it still has oldIP,
// and logs the change in the same transaction.
func (r *TenancyRepository) MovePrinterIP(ctx context.Context, locationID, printerID uuid.UUID, oldIP, newIP, mac, source string) (*tenancy.Printer, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := models.New(tx)
	row, err := q.MovePrinterIP(ctx, models.MovePrinterIPParams{
		NewIP:      newIP,
		MAC:        mac,
		ID:         pgtype.UUID{Bytes: printerID, Valid: true},
		LocationID: pgtype.UUID{Bytes: locationID, Valid: true},
		OldIP:      oldIP,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tenancy.ErrPrinterIPChanged
	}
	if err != nil {
		return nil, fmt.Errorf("move printer ip: %w", err)
	}
	if err := q.InsertPrinterIPChange(ctx, models.InsertPrinterIPChangeParams{
		TenantID:   row.TenantID,
		LocationID: row.LocationID,
		PrinterID:  row.ID,
		OldIP:      oldIP,
		NewIP:      newIP,
		MAC:        row.MAC,
		Source:     source,
	}); err != nil {
		return nil, fmt.Errorf("log printer ip change: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}
	return toDomainPrinter(row), nil
}

func (r *TenancyRepository) SetPrinterMAC(ctx context.Context, printerID uuid.UUID, mac string) error {
	if err := models.New(r.pool).SetPrinterMAC(ctx, models.SetPrinterMACParams{
		ID: pgtype.UUID{Bytes: printerID, Valid: true}, MAC: mac,
	}); err != nil {
		return fmt.Errorf("set printer mac: %w", err)
	}
	return nil
}

func (r *TenancyRepository) InsertPrinterIPChange(ctx context.Context, c tenancy.PrinterIPChange) error {
	if err := models.New(r.pool).InsertPrinterIPChange(ctx, models.InsertPrinterIPChangeParams{
		TenantID:   pgtype.UUID{Bytes: c.TenantID, Valid: true},
		LocationID: pgtype.UUID{Bytes: c.LocationID, Valid: true},
		PrinterID:  pgtype.UUID{Bytes: c.PrinterID, Valid: true},
		OldIP:      c.OldIP,
		NewIP:      c.NewIP,
		MAC:        c.MAC,
		Source:     c.Source,
	}); err != nil {
		return fmt.Errorf("log printer ip change: %w", err)
	}
	return nil
}

func (r *TenancyRepository) ListPrinterIPChanges(ctx context.Context, locationID uuid.UUID, limit int) ([]tenancy.PrinterIPChange, error) {
	rows, err := models.New(r.pool).ListPrinterIPChanges(ctx, models.ListPrinterIPChangesParams{
		LocationID: pgtype.UUID{Bytes: locationID, Valid: true}, MaxRows: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list printer ip changes: %w", err)
	}
	out := make([]tenancy.PrinterIPChange, 0, len(rows))
	for _, row := range rows {
		c := tenancy.PrinterIPChange{
			ID:          uuid.UUID(row.ID.Bytes),
			LocationID:  locationID,
			PrinterID:   uuid.UUID(row.PrinterID.Bytes),
			PrinterName: row.PrinterName,
			OldIP:       row.OldIP,
			NewIP:       row.NewIP,
			MAC:         row.MAC,
			Source:      row.Source,
		}
		if row.At.Valid {
			c.At = row.At.Time
		}
		out = append(out, c)
	}
	return out, nil
}

func (r *TenancyRepository) DeletePrintersByLocation(ctx context.Context, locationID uuid.UUID) error {
	q := models.New(r.pool)
	if err := q.DeletePrintersByLocation(ctx, pgtype.UUID{Bytes: locationID, Valid: true}); err != nil {
		return fmt.Errorf("delete printers by location: %w", err)
	}
	return nil
}

// --- Mappers ---------------------------------------------------------------

func toDomainTenant(row models.Tenant) *tenancy.Tenant {
	t := &tenancy.Tenant{
		ID:     uuid.UUID(row.ID.Bytes),
		Slug:   row.Slug,
		Name:   row.Name,
		Config: unmarshalConfig(row.Config),
	}
	if row.Username.Valid {
		t.Username = row.Username.String
	}
	if row.StoreID.Valid {
		id := row.StoreID.Int64
		t.StoreID = &id
	}
	if row.CreatedAt.Valid {
		t.CreatedAt = row.CreatedAt.Time
	}
	return t
}

func toDomainLocation(row models.Location) *tenancy.Location {
	l := &tenancy.Location{
		ID:             uuid.UUID(row.ID.Bytes),
		TenantID:       uuid.UUID(row.TenantID.Bytes),
		Slug:           row.Slug,
		Name:           row.Name,
		Timezone:       row.Timezone,
		DailyResetHour: int(row.DailyResetHour),
		Phone:          row.Phone,
		AddressLine1:   row.AddressLine1,
		AddressLine2:   row.AddressLine2,
		Config:         unmarshalConfig(row.Config),

		GeoSource:       row.GeoSource,
		DeliveryRadiusM: int(row.DeliveryRadiusM),
		MapURL:          row.MapUrl,
		MapSHA256:       row.MapSha256,
		MapVersion:      row.MapVersion,
		MapBBox:         row.MapBbox,
	}
	if row.CreatedAt.Valid {
		l.CreatedAt = row.CreatedAt.Time
	}
	if row.Lat.Valid && row.Lng.Valid {
		lat, lng := row.Lat.Float64, row.Lng.Float64
		l.Lat, l.Lng = &lat, &lng
	}
	return l
}

func toDomainTenancyDevice(row models.Device) *tenancy.Device {
	d := &tenancy.Device{
		ID:         uuid.UUID(row.ID.Bytes),
		TenantID:   uuid.UUID(row.TenantID.Bytes),
		LocationID: uuid.UUID(row.LocationID.Bytes),
		Name:       row.Name,
		Kind:       row.Kind,
		KeyPrefix:  row.KeyPrefix,
	}
	if row.CreatedAt.Valid {
		d.CreatedAt = row.CreatedAt.Time
	}
	if row.LastUsedAt.Valid {
		t := row.LastUsedAt.Time
		d.LastUsedAt = &t
	}
	if row.RevokedAt.Valid {
		t := row.RevokedAt.Time
		d.RevokedAt = &t
	}
	return d
}

func toDomainPrinter(row models.Printer) *tenancy.Printer {
	p := &tenancy.Printer{
		ID:         uuid.UUID(row.ID.Bytes),
		TenantID:   uuid.UUID(row.TenantID.Bytes),
		LocationID: uuid.UUID(row.LocationID.Bytes),
		Name:       row.Name,
		IP:         row.IP,
		IsReceipt:  row.IsReceipt,
		MAC:        row.MAC,
	}
	if row.CreatedAt.Valid {
		p.CreatedAt = row.CreatedAt.Time
	}
	if row.UpdatedAt.Valid {
		p.UpdatedAt = row.UpdatedAt.Time
	}
	return p
}

// unmarshalConfig decodes the jsonb column. nil/empty maps to an empty
// config so callers never have to nil-check.
func unmarshalConfig(raw []byte) tenancy.Config {
	if len(raw) == 0 {
		return tenancy.Config{}
	}
	var c tenancy.Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return tenancy.Config{}
	}
	return c
}

// marshalConfig encodes for the jsonb column. nil maps -> "{}" so we never
// write a NULL JSON value.
func marshalConfig(c tenancy.Config) ([]byte, error) {
	if c == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(c)
}
