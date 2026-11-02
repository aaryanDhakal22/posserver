package repositories

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"quiccpos/main/internal/domain/channel"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var _ channel.Repository = (*ChannelRepository)(nil)

// ChannelRepository persists sales channels (main 3.0).
type ChannelRepository struct {
	pool *pgxpool.Pool
}

func NewChannelRepository(pool *pgxpool.Pool) *ChannelRepository {
	return &ChannelRepository{pool: pool}
}

func (r *ChannelRepository) Create(ctx context.Context, c channel.NewChannel) (*channel.Channel, error) {
	perms, err := json.Marshal(c.Permissions)
	if err != nil {
		return nil, err
	}
	var hash pgtype.Text
	if c.KeyHash != "" {
		hash = pgtype.Text{String: c.KeyHash, Valid: true}
	}
	row, err := models.New(r.pool).CreateChannel(ctx, models.CreateChannelParams{
		TenantID: pgID(c.TenantID), LocationID: pgID(c.LocationID), Kind: string(c.Kind), Name: c.Name,
		KeyHash: hash, KeyPrefix: c.KeyPrefix, Permissions: perms, Builtin: c.Builtin,
	})
	if err != nil {
		return nil, fmt.Errorf("create channel: %w", err)
	}
	return channelOf(row)
}

func (r *ChannelRepository) Get(ctx context.Context, id uuid.UUID) (*channel.Channel, error) {
	row, err := models.New(r.pool).GetChannel(ctx, pgID(id))
	if err != nil {
		return nil, err
	}
	return channelOf(row)
}

func (r *ChannelRepository) List(ctx context.Context, locationID *uuid.UUID) ([]channel.Channel, error) {
	q := models.New(r.pool)
	var rows []models.Channel
	var err error
	if locationID != nil {
		rows, err = q.ListChannelsByLocation(ctx, pgID(*locationID))
	} else {
		rows, err = q.ListChannels(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	out := make([]channel.Channel, 0, len(rows))
	for _, row := range rows {
		c, err := channelOf(row)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, nil
}

func (r *ChannelRepository) Update(ctx context.Context, id uuid.UUID, name string, p channel.Permissions) (*channel.Channel, error) {
	perms, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	row, err := models.New(r.pool).UpdateChannel(ctx, models.UpdateChannelParams{ID: pgID(id), Name: name, Permissions: perms})
	if err != nil {
		return nil, err
	}
	return channelOf(row)
}

func (r *ChannelRepository) SetKey(ctx context.Context, id uuid.UUID, keyHash, keyPrefix string) (*channel.Channel, error) {
	row, err := models.New(r.pool).SetChannelKey(ctx, models.SetChannelKeyParams{
		ID: pgID(id), KeyHash: pgtype.Text{String: keyHash, Valid: true}, KeyPrefix: keyPrefix})
	if err != nil {
		return nil, err
	}
	return channelOf(row)
}

func (r *ChannelRepository) Revoke(ctx context.Context, id uuid.UUID) (*channel.Channel, error) {
	row, err := models.New(r.pool).RevokeChannel(ctx, pgID(id))
	if err != nil {
		return nil, err
	}
	return channelOf(row)
}

func (r *ChannelRepository) Delete(ctx context.Context, id uuid.UUID) (bool, error) {
	n, err := models.New(r.pool).DeleteChannel(ctx, pgID(id))
	return n > 0, err
}

func (r *ChannelRepository) Authenticate(ctx context.Context, keyHash string) (*channel.Channel, error) {
	row, err := models.New(r.pool).AuthenticateChannel(ctx, pgtype.Text{String: keyHash, Valid: true})
	if err != nil {
		return nil, err
	}
	return channelOf(row)
}

func (r *ChannelRepository) Touch(ctx context.Context, id uuid.UUID) error {
	return models.New(r.pool).TouchChannel(ctx, pgID(id))
}

func channelOf(row models.Channel) (*channel.Channel, error) {
	c := &channel.Channel{
		ID: idOf(row.ID), TenantID: idOf(row.TenantID), LocationID: idOf(row.LocationID),
		Kind: channel.Kind(row.Kind), Name: row.Name, KeyPrefix: row.KeyPrefix, Builtin: row.Builtin,
		RevokedAt: tsPtr(row.RevokedAt), LastUsedAt: tsPtr(row.LastUsedAt),
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
	if err := json.Unmarshal(row.Permissions, &c.Permissions); err != nil {
		return nil, fmt.Errorf("channel %s permissions: %w", c.ID, err)
	}
	if c.Permissions.Payments == nil {
		c.Permissions.Payments = []string{}
	}
	return c, nil
}

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}
