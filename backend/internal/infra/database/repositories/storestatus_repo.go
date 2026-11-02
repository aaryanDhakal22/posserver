package repositories

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"quiccpos/main/internal/domain/storestatus"
	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StoreStatusRepository persists a location's ordering settings + pause,
// its dated hours exceptions, and the daily restock of 86'd items (main 3.0
// step 3).
type StoreStatusRepository struct {
	pool *pgxpool.Pool
}

func NewStoreStatusRepository(pool *pgxpool.Pool) *StoreStatusRepository {
	return &StoreStatusRepository{pool: pool}
}

func tsOf(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func settingsOf(row models.StoreOrdering) storestatus.Settings {
	return storestatus.Settings{
		PickupEnabled: row.PickupEnabled, DeliveryEnabled: row.DeliveryEnabled,
		PickupETAMinutes: int(row.PickupEtaMinutes), DeliveryETAMinutes: int(row.DeliveryEtaMinutes),
		ScheduleDays: int(row.ScheduleDays), PausedAt: tsPtr(row.PausedAt), PausedUntil: tsPtr(row.PausedUntil), PauseNote: row.PauseNote,
	}
}

// Settings returns a location's ordering row, created with the defaults on
// first use.
func (r *StoreStatusRepository) Settings(ctx context.Context, tenantID, locationID uuid.UUID) (storestatus.Settings, error) {
	q := models.New(r.pool)
	if err := q.EnsureStoreOrdering(ctx, models.EnsureStoreOrderingParams{LocationID: pgID(locationID), TenantID: pgID(tenantID)}); err != nil {
		return storestatus.Settings{}, err
	}
	row, err := q.GetStoreOrdering(ctx, pgID(locationID))
	if err != nil {
		return storestatus.Settings{}, err
	}
	return settingsOf(row), nil
}

func (r *StoreStatusRepository) UpdateSettings(ctx context.Context, locationID uuid.UUID, s storestatus.Settings) (storestatus.Settings, error) {
	row, err := models.New(r.pool).UpdateStoreOrdering(ctx, models.UpdateStoreOrderingParams{
		LocationID: pgID(locationID), PickupEnabled: s.PickupEnabled, DeliveryEnabled: s.DeliveryEnabled,
		PickupEtaMinutes: int32(s.PickupETAMinutes), DeliveryEtaMinutes: int32(s.DeliveryETAMinutes), ScheduleDays: int32(s.ScheduleDays),
	})
	if err != nil {
		return storestatus.Settings{}, err
	}
	return settingsOf(row), nil
}

func (r *StoreStatusRepository) SetPause(ctx context.Context, locationID uuid.UUID, at time.Time, until *time.Time, note string) (storestatus.Settings, error) {
	row, err := models.New(r.pool).SetStorePause(ctx, models.SetStorePauseParams{
		LocationID: pgID(locationID), PausedAt: tsOf(&at), PausedUntil: tsOf(until), PauseNote: note})
	if err != nil {
		return storestatus.Settings{}, err
	}
	return settingsOf(row), nil
}

func (r *StoreStatusRepository) ClearPause(ctx context.Context, locationID uuid.UUID) (storestatus.Settings, error) {
	row, err := models.New(r.pool).ClearStorePause(ctx, pgID(locationID))
	if err != nil {
		return storestatus.Settings{}, err
	}
	return settingsOf(row), nil
}

// --- hours exceptions ---------------------------------------------------------------

func exceptionOf(row models.StoreHoursException) (storestatus.Exception, error) {
	e := storestatus.Exception{ID: idOf(row.ID), Date: row.Day.Time.Format(time.DateOnly), Closed: row.Closed, Note: row.Note}
	if err := json.Unmarshal(row.Hours, &e.Hours); err != nil {
		return e, fmt.Errorf("exception %s hours: %w", e.ID, err)
	}
	if e.Hours == nil {
		e.Hours = []tenancy.HoursRange{}
	}
	return e, nil
}

// Exceptions lists a location's exceptions on or after a date.
func (r *StoreStatusRepository) Exceptions(ctx context.Context, locationID uuid.UUID, from time.Time) ([]storestatus.Exception, error) {
	rows, err := models.New(r.pool).ListStoreHoursExceptions(ctx, models.ListStoreHoursExceptionsParams{
		LocationID: pgID(locationID), Day: pgtype.Date{Time: from, Valid: true}})
	if err != nil {
		return nil, err
	}
	out := make([]storestatus.Exception, 0, len(rows))
	for _, row := range rows {
		e, err := exceptionOf(row)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// SetException adds a date's exception or replaces the one it has.
func (r *StoreStatusRepository) SetException(ctx context.Context, tenantID, locationID uuid.UUID, e storestatus.Exception) (storestatus.Exception, error) {
	day, err := time.Parse(time.DateOnly, e.Date)
	if err != nil {
		return e, err
	}
	hours, err := json.Marshal(e.Hours)
	if err != nil {
		return e, err
	}
	row, err := models.New(r.pool).UpsertStoreHoursException(ctx, models.UpsertStoreHoursExceptionParams{
		TenantID: pgID(tenantID), LocationID: pgID(locationID), Day: pgtype.Date{Time: day, Valid: true},
		Closed: e.Closed, Hours: hours, Note: e.Note})
	if err != nil {
		return e, err
	}
	return exceptionOf(row)
}

func (r *StoreStatusRepository) DeleteException(ctx context.Context, locationID, id uuid.UUID) (bool, error) {
	n, err := models.New(r.pool).DeleteStoreHoursException(ctx, models.DeleteStoreHoursExceptionParams{ID: pgID(id), LocationID: pgID(locationID)})
	return n > 0, err
}

// --- sold out + restock ---------------------------------------------------------------

func (r *StoreStatusRepository) SoldOut(ctx context.Context, locationID uuid.UUID) ([]storestatus.SoldOutItem, error) {
	rows, err := models.New(r.pool).ListSoldOutItems(ctx, pgID(locationID))
	if err != nil {
		return nil, err
	}
	out := make([]storestatus.SoldOutItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, storestatus.SoldOutItem{ItemID: idOf(row.ID), Name: row.Name, Until: tsPtr(row.OutUntil)})
	}
	return out, nil
}

// MenuVersion is the location's menu version (0 before its first menu write).
func (r *StoreStatusRepository) MenuVersion(ctx context.Context, locationID uuid.UUID) (int64, error) {
	v, err := models.New(r.pool).GetMenuVersion(ctx, pgID(locationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return v.Version, err
}

// RestockRow is what the restock loop reads per location.
type RestockRow struct {
	LocationID     uuid.UUID
	RestockedAt    *time.Time
	Timezone       string
	DailyResetHour int
}

func (r *StoreStatusRepository) EnsureAll(ctx context.Context, locs []tenancy.Location) error {
	q := models.New(r.pool)
	for _, l := range locs {
		if err := q.EnsureStoreOrdering(ctx, models.EnsureStoreOrderingParams{LocationID: pgID(l.ID), TenantID: pgID(l.TenantID)}); err != nil {
			return err
		}
	}
	return nil
}

func (r *StoreStatusRepository) RestockRows(ctx context.Context) ([]RestockRow, error) {
	rows, err := models.New(r.pool).ListStoreOrderingForRestock(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RestockRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, RestockRow{LocationID: idOf(row.LocationID), RestockedAt: tsPtr(row.RestockedAt),
			Timezone: row.Timezone, DailyResetHour: int(row.DailyResetHour)})
	}
	return out, nil
}

// Restock brings a location's 86'd items (without a later out_until) and
// options back and stamps the time, in one tx. It returns how many came back.
func (r *StoreStatusRepository) Restock(ctx context.Context, locationID uuid.UUID, at time.Time) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		q := models.New(tx)
		items, err := q.RestockItems(ctx, pgID(locationID))
		if err != nil {
			return err
		}
		opts, err := q.RestockOptions(ctx, pgID(locationID))
		if err != nil {
			return err
		}
		n = items + opts
		return q.SetStoreRestocked(ctx, models.SetStoreRestockedParams{LocationID: pgID(locationID), RestockedAt: tsOf(&at)})
	})
	return n, err
}

// ExpireOutUntil brings back every item whose out_until has passed.
func (r *StoreStatusRepository) ExpireOutUntil(ctx context.Context) (int64, error) {
	return models.New(r.pool).ExpireOutUntil(ctx)
}
