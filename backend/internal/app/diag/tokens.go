package diag

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	authSvc "quiccpos/main/internal/app/auth"
	"quiccpos/main/internal/infra/database/models"
	sseBroker "quiccpos/main/internal/infra/sse"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// TokenPrefix marks a tablet report token (X-Quibase-Report-Key).
const TokenPrefix = "qrep_"

// keyPrefixLen is how much of a token is kept in clear for the admin list.
const keyPrefixLen = 12

// generateToken returns a fresh token: "qrep_" + 32 random bytes, base64url.
func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return TokenPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// TabletAuth is who a valid report token belongs to.
type TabletAuth struct {
	TokenID    uuid.UUID
	Scope      sseBroker.Scope
	TabletID   string
	TabletName string
}

// Mint issues a report token for one tablet of scope's location: the
// tablet's previous token is revoked, and past MaxActiveTokens the location's
// oldest are. Returns the token row id and the cleartext token (shown once;
// only its hash is stored).
func (s *Service) Mint(ctx context.Context, scope sseBroker.Scope, tabletID, tabletName string) (uuid.UUID, string, error) {
	tabletID = clip(strings.TrimSpace(tabletID), maxTabletID)
	if tabletID == "" {
		return uuid.Nil, "", ErrBadTablet
	}
	tabletName = clip(strings.TrimSpace(tabletName), maxTabletName)
	token, err := generateToken()
	if err != nil {
		return uuid.Nil, "", err
	}
	var id pgtype.UUID
	err = s.tx(ctx, func(q Queries) error {
		if _, err := q.RevokeTabletTokensForTablet(ctx, models.RevokeTabletTokensForTabletParams{
			LocationID: pgUUID(scope.LocationID), TabletID: tabletID,
		}); err != nil {
			return err
		}
		var err error
		id, err = q.InsertTabletReportToken(ctx, models.InsertTabletReportTokenParams{
			TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID),
			TabletID: tabletID, TabletName: tabletName,
			TokenHash: authSvc.HashKey(token), KeyPrefix: token[:keyPrefixLen],
		})
		if err != nil {
			return err
		}
		_, err = q.RevokeExcessTabletTokens(ctx, models.RevokeExcessTabletTokensParams{
			LocationID: pgUUID(scope.LocationID), Keep: MaxActiveTokens,
		})
		return err
	})
	if err != nil {
		return uuid.Nil, "", err
	}
	s.logger.Info().Ctx(ctx).Str("location_id", scope.LocationID.String()).Str("tablet_id", tabletID).
		Str("key_prefix", token[:keyPrefixLen]).Msg("tablet report token minted")
	return fromPg(id), token, nil
}

// Revoke revokes a tablet's active token in scope (idempotent).
func (s *Service) Revoke(ctx context.Context, scope sseBroker.Scope, tabletID string) error {
	_, err := s.q.RevokeTabletTokensForTablet(ctx, models.RevokeTabletTokensForTabletParams{
		LocationID: pgUUID(scope.LocationID), TabletID: strings.TrimSpace(tabletID),
	})
	return err
}

// Authenticate resolves a cleartext report token. last_used_at is written at
// most once a minute.
func (s *Service) Authenticate(ctx context.Context, token string) (*TabletAuth, error) {
	if !strings.HasPrefix(token, TokenPrefix) {
		return nil, ErrInvalidToken
	}
	row, err := s.q.GetActiveTabletTokenByHash(ctx, authSvc.HashKey(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidToken
	}
	if err != nil {
		return nil, err
	}
	if !row.LastUsedAt.Valid || s.now().Sub(row.LastUsedAt.Time) >= touchEvery {
		if err := s.q.TouchTabletToken(ctx, row.ID); err != nil {
			s.logger.Debug().Err(err).Msg("touch report token failed")
		}
	}
	return &TabletAuth{
		TokenID:    fromPg(row.ID),
		Scope:      sseBroker.Scope{TenantID: fromPg(row.TenantID), LocationID: fromPg(row.LocationID)},
		TabletID:   row.TabletID,
		TabletName: row.TabletName,
	}, nil
}

// TokenInfo is one report token as the admin list shows it (never the hash).
type TokenInfo struct {
	ID           uuid.UUID  `json:"id"`
	TenantID     uuid.UUID  `json:"tenant_id"`
	LocationID   uuid.UUID  `json:"location_id"`
	LocationName string     `json:"location_name"`
	TabletID     string     `json:"tablet_id"`
	TabletName   string     `json:"tablet_name"`
	KeyPrefix    string     `json:"key_prefix"`
	CreatedAt    time.Time  `json:"created_at"`
	LastUsedAt   *time.Time `json:"last_used_at"`
	RevokedAt    *time.Time `json:"revoked_at"`
}

// ListTokens lists report tokens (newest first), optionally for one location.
func (s *Service) ListTokens(ctx context.Context, locationID *uuid.UUID) ([]TokenInfo, error) {
	var loc pgtype.UUID
	if locationID != nil {
		loc = pgUUID(*locationID)
	}
	rows, err := s.q.ListTabletTokens(ctx, loc)
	if err != nil {
		return nil, err
	}
	out := make([]TokenInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, TokenInfo{
			ID: fromPg(r.ID), TenantID: fromPg(r.TenantID), LocationID: fromPg(r.LocationID),
			LocationName: r.LocationName, TabletID: r.TabletID, TabletName: r.TabletName,
			KeyPrefix: r.KeyPrefix, CreatedAt: r.CreatedAt.Time,
			LastUsedAt: tsPtr(r.LastUsedAt), RevokedAt: tsPtr(r.RevokedAt),
		})
	}
	return out, nil
}

// RevokeTokenByID revokes one token by its row id (admin; idempotent).
func (s *Service) RevokeTokenByID(ctx context.Context, id uuid.UUID) error {
	n, err := s.q.RevokeTabletTokenByID(ctx, pgUUID(id))
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
