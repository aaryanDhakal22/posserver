// Package operatorApp is the use-case layer for operator (call-center) accounts:
// login, revocable read-only tokens, allowed-location management.
package operatorApp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	authSvc "quiccpos/main/internal/app/auth"
	tenancyApp "quiccpos/main/internal/app/tenancy"
	operatorDom "quiccpos/main/internal/domain/operator"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrInvalidToken       = errors.New("invalid or expired token")
)

type Service struct {
	repo    operatorDom.Repository
	tenancy *tenancyApp.Service
	logger  zerolog.Logger
}

func NewService(repo operatorDom.Repository, tenancy *tenancyApp.Service, lg zerolog.Logger) *Service {
	return &Service{repo: repo, tenancy: tenancy, logger: lg.With().Str("module", "operator-service").Logger()}
}

// --- admin management ------------------------------------------------------

func (s *Service) List(ctx context.Context) ([]operatorDom.Operator, error) {
	return s.repo.ListOperators(ctx)
}

func (s *Service) Create(ctx context.Context, username, password, displayName string) (*operatorDom.Operator, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, errors.New("username and password are required")
	}
	h, err := authSvc.HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	return s.repo.CreateOperator(ctx, username, h, strings.TrimSpace(displayName))
}

func (s *Service) UpdateProfile(ctx context.Context, id uuid.UUID, displayName string) (*operatorDom.Operator, error) {
	return s.repo.UpdateOperatorProfile(ctx, id, strings.TrimSpace(displayName))
}

func (s *Service) SetPassword(ctx context.Context, id uuid.UUID, password string) error {
	if password == "" {
		return errors.New("password is required")
	}
	h, err := authSvc.HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	return s.repo.SetOperatorPassword(ctx, id, h)
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	return s.repo.DeleteOperator(ctx, id)
}

// SetAllowedLocations replaces the operator's allowed set, resolving each
// location's tenant (and rejecting unknown location ids).
func (s *Service) SetAllowedLocations(ctx context.Context, operatorID uuid.UUID, locationIDs []uuid.UUID) error {
	locs := make([]operatorDom.AllowedLocation, 0, len(locationIDs))
	for _, lid := range locationIDs {
		loc, err := s.tenancy.GetLocation(ctx, lid)
		if err != nil {
			return fmt.Errorf("location %s not found", lid)
		}
		locs = append(locs, operatorDom.AllowedLocation{TenantID: loc.TenantID, LocationID: loc.ID})
	}
	return s.repo.SetAllowedLocations(ctx, operatorID, locs)
}

func (s *Service) ListAllowedLocations(ctx context.Context, operatorID uuid.UUID) ([]operatorDom.AllowedLocation, error) {
	return s.repo.ListAllowedLocations(ctx, operatorID)
}

func (s *Service) RevokeTokens(ctx context.Context, operatorID uuid.UUID) error {
	return s.repo.RevokeAllTokens(ctx, operatorID)
}

// --- login + token ---------------------------------------------------------

func (s *Service) Authenticate(ctx context.Context, username, password string) (*operatorDom.Operator, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, ErrInvalidCredentials
	}
	auth, err := s.repo.GetOperatorAuth(ctx, username)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("lookup operator: %w", err)
	}
	ok, needsRehash := authSvc.VerifyPassword(password, auth.PasswordHash)
	if !ok {
		return nil, ErrInvalidCredentials
	}
	if needsRehash {
		// Legacy SHA-256 hash: upgrade to bcrypt (best-effort, never fails login).
		if h, err := authSvc.HashPassword(password); err == nil {
			_ = s.repo.SetOperatorPassword(ctx, auth.ID, h)
		}
	}
	return s.repo.GetOperator(ctx, auth.ID)
}

// MintToken creates a fresh read-only bearer token and returns the cleartext
// once (only the hash is stored).
func (s *Service) MintToken(ctx context.Context, operatorID uuid.UUID) (string, error) {
	raw, err := generateToken()
	if err != nil {
		return "", err
	}
	if err := s.repo.CreateToken(ctx, operatorID, authSvc.HashKey(raw), raw[:12], nil); err != nil {
		return "", err
	}
	return raw, nil
}

// AuthenticateToken resolves a bearer token to its operator + allowed locations.
// No caching → revocation takes effect immediately.
func (s *Service) AuthenticateToken(ctx context.Context, rawToken string) (*operatorDom.Operator, []operatorDom.AllowedLocation, error) {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return nil, nil, ErrInvalidToken
	}
	ta, err := s.repo.AuthenticateToken(ctx, authSvc.HashKey(rawToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrInvalidToken
		}
		return nil, nil, fmt.Errorf("token lookup: %w", err)
	}
	// Fire-and-forget last-used stamp (best effort).
	go func(id uuid.UUID) { _ = s.repo.TouchToken(context.Background(), id) }(ta.TokenID)

	op := &operatorDom.Operator{ID: ta.OperatorID, Username: ta.Username, DisplayName: ta.DisplayName}
	allowed, err := s.repo.ListAllowedLocations(ctx, ta.OperatorID)
	if err != nil {
		return nil, nil, err
	}
	return op, allowed, nil
}

func generateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "qopr_" + hex.EncodeToString(buf), nil
}

func IsInvalidToken(err error) bool       { return errors.Is(err, ErrInvalidToken) }
func IsInvalidCredentials(err error) bool { return errors.Is(err, ErrInvalidCredentials) }
