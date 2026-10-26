package repositories

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"quiccpos/main/internal/infra/database/models"
)

// StoreSlugs returns the lookup behind the metric store labels
// (observability.SetStoreLookup): (tenant slug, location slug) by a tenant id
// or a location id; uuid.Nil for the other one never matches a row.
func StoreSlugs(pool *pgxpool.Pool) func(ctx context.Context, tenantID, locationID uuid.UUID) (string, string, error) {
	return func(ctx context.Context, tenantID, locationID uuid.UUID) (string, string, error) {
		row, err := models.New(pool).GetStoreSlugs(ctx, models.GetStoreSlugsParams{
			TenantID:   pgtype.UUID{Bytes: tenantID, Valid: true},
			LocationID: pgtype.UUID{Bytes: locationID, Valid: true},
		})
		if err != nil {
			return "", "", err
		}
		return row.TenantSlug, row.LocationSlug, nil
	}
}
