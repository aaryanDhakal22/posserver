package repositories

import (
	"context"
	"fmt"

	domainAuth "quiccpos/main/internal/domain/auth"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

var _ domainAuth.Repository = (*DeviceAuthRepository)(nil)

// DeviceAuthRepository is the read-side of the devices table consumed by the
// auth middleware. Writes (mint/revoke) flow through the tenancy admin repo
// to keep the surfaces orthogonal — auth never mutates anything but
// last_used_at.
type DeviceAuthRepository struct {
	pool   *pgxpool.Pool
	logger zerolog.Logger
}

func NewDeviceAuthRepository(pool *pgxpool.Pool, lg zerolog.Logger) *DeviceAuthRepository {
	return &DeviceAuthRepository{
		pool:   pool,
		logger: lg.With().Str("module", "auth-repo").Logger(),
	}
}

func (r *DeviceAuthRepository) GetActiveByKeyHash(ctx context.Context, hash string) (*domainAuth.Device, error) {
	q := models.New(r.pool)
	row, err := q.GetActiveDeviceByKeyHash(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("get device by key hash: %w", err)
	}
	return toDomainDevice(row), nil
}

func (r *DeviceAuthRepository) Touch(ctx context.Context, id uuid.UUID) error {
	q := models.New(r.pool)
	if err := q.TouchDevice(ctx, pgtype.UUID{Bytes: id, Valid: true}); err != nil {
		return fmt.Errorf("touch device: %w", err)
	}
	return nil
}

func toDomainDevice(row models.Device) *domainAuth.Device {
	d := &domainAuth.Device{
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
