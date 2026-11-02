package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"quiccpos/main/internal/domain/pricing"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrCodeTaken: another of the store's deals has that code.
var ErrCodeTaken = errors.New("another deal has that code")

// PricingRepository persists a location's pricing settings and deals (main
// 3.0 step 4).
type PricingRepository struct {
	pool *pgxpool.Pool
}

func NewPricingRepository(pool *pgxpool.Pool) *PricingRepository {
	return &PricingRepository{pool: pool}
}

// Settings reads the store's settings (nil = never saved) and the kinds'
// overrides that exist.
func (r *PricingRepository) Settings(ctx context.Context, locationID uuid.UUID) (*pricing.StoreSettings, map[string]pricing.KindOverride, error) {
	rows, err := models.New(r.pool).ListPricingSettings(ctx, pgID(locationID))
	if err != nil {
		return nil, nil, err
	}
	var store *pricing.StoreSettings
	over := map[string]pricing.KindOverride{}
	for _, row := range rows {
		if row.Kind == "" {
			var s pricing.StoreSettings
			if err := json.Unmarshal(row.Settings, &s); err != nil {
				return nil, nil, fmt.Errorf("pricing settings: %w", err)
			}
			store = &s
			continue
		}
		var o pricing.KindOverride
		if err := json.Unmarshal(row.Settings, &o); err != nil {
			return nil, nil, fmt.Errorf("pricing settings %s: %w", row.Kind, err)
		}
		over[row.Kind] = o
	}
	return store, over, nil
}

// SaveSettings replaces the store's settings and every kind's override (an
// empty override is removed).
func (r *PricingRepository) SaveSettings(ctx context.Context, tenantID, locationID uuid.UUID, s pricing.StoreSettings, over map[string]pricing.KindOverride) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		q := models.New(tx)
		raw, err := json.Marshal(s)
		if err != nil {
			return err
		}
		if err := q.UpsertPricingSettings(ctx, models.UpsertPricingSettingsParams{LocationID: pgID(locationID), TenantID: pgID(tenantID), Kind: "", Settings: raw}); err != nil {
			return err
		}
		for _, k := range pricing.Kinds {
			o, ok := over[k]
			if !ok || (o.Fees == nil && o.Tips == nil && o.CardFee == nil) {
				if err := q.DeletePricingSettings(ctx, models.DeletePricingSettingsParams{LocationID: pgID(locationID), Kind: k}); err != nil {
					return err
				}
				continue
			}
			raw, err := json.Marshal(o)
			if err != nil {
				return err
			}
			if err := q.UpsertPricingSettings(ctx, models.UpsertPricingSettingsParams{LocationID: pgID(locationID), TenantID: pgID(tenantID), Kind: k, Settings: raw}); err != nil {
				return err
			}
		}
		return nil
	})
}

func dealOf(row models.Deal) (pricing.StoreDeal, error) {
	var d pricing.StoreDeal
	if err := json.Unmarshal(row.Def, &d.Deal); err != nil {
		return d, fmt.Errorf("deal %s: %w", idOf(row.ID), err)
	}
	d.ID, d.Kind, d.Name, d.Description, d.Active, d.Code = idOf(row.ID).String(), row.Kind, row.Name, row.Description, row.Active, row.Code
	d.ChannelKinds, d.Position = row.ChannelKinds, int(row.Position)
	if d.ChannelKinds == nil {
		d.ChannelKinds = []string{}
	}
	d.CreatedAt, d.UpdatedAt = row.CreatedAt.Time, row.UpdatedAt.Time
	return d, nil
}

func (r *PricingRepository) Deals(ctx context.Context, locationID uuid.UUID) ([]pricing.StoreDeal, error) {
	rows, err := models.New(r.pool).ListDeals(ctx, pgID(locationID))
	if err != nil {
		return nil, err
	}
	out := make([]pricing.StoreDeal, 0, len(rows))
	for _, row := range rows {
		d, err := dealOf(row)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func (r *PricingRepository) Deal(ctx context.Context, locationID, id uuid.UUID) (pricing.StoreDeal, error) {
	row, err := models.New(r.pool).GetDeal(ctx, models.GetDealParams{ID: pgID(id), LocationID: pgID(locationID)})
	if err != nil {
		return pricing.StoreDeal{}, err
	}
	return dealOf(row)
}

func codeTaken(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrCodeTaken
	}
	return err
}

func (r *PricingRepository) CreateDeal(ctx context.Context, tenantID, locationID uuid.UUID, d pricing.StoreDeal) (pricing.StoreDeal, error) {
	def, err := json.Marshal(d.Deal)
	if err != nil {
		return d, err
	}
	row, err := models.New(r.pool).CreateDeal(ctx, models.CreateDealParams{TenantID: pgID(tenantID), LocationID: pgID(locationID),
		Kind: d.Kind, Name: d.Name, Description: d.Description, Active: d.Active, ChannelKinds: d.ChannelKinds, Def: def, Code: d.Code})
	if err != nil {
		return d, codeTaken(err)
	}
	return dealOf(row)
}

func (r *PricingRepository) UpdateDeal(ctx context.Context, locationID, id uuid.UUID, d pricing.StoreDeal) (pricing.StoreDeal, error) {
	def, err := json.Marshal(d.Deal)
	if err != nil {
		return d, err
	}
	row, err := models.New(r.pool).UpdateDeal(ctx, models.UpdateDealParams{ID: pgID(id), LocationID: pgID(locationID),
		Kind: d.Kind, Name: d.Name, Description: d.Description, Active: d.Active, ChannelKinds: d.ChannelKinds, Def: def, Code: d.Code})
	if err != nil {
		return d, codeTaken(err)
	}
	return dealOf(row)
}

func (r *PricingRepository) DeleteDeal(ctx context.Context, locationID, id uuid.UUID) (bool, error) {
	n, err := models.New(r.pool).DeleteDeal(ctx, models.DeleteDealParams{ID: pgID(id), LocationID: pgID(locationID)})
	return n > 0, err
}
