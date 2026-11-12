package squarepay

import (
	"context"
	"errors"
	"strings"
	"time"

	"quiccpos/main/internal/infra/database/repositories"
	"quiccpos/main/internal/infra/square"

	"github.com/google/uuid"
)

// Paste a Square sandbox token (staging / dev only, 2026-11-12): the owner
// pastes the sandbox access token (Square Developer Console › the app ›
// Sandbox) + a sandbox location id instead of the OAuth round trip. It is
// stored exactly like an OAuth-linked store (sealed with SQUARE_TOKEN_KEY, the
// same square_connections row, connected), so website holds, the terminal's
// reader session, keyed payments and refunds all use it. The refresh token is
// the marker pastedRefresh: nothing is refreshed or revoked. Refused unless
// this server runs Square's sandbox (or the dev fake); never logged.

// pastedRefresh is the sealed "refresh token" of a pasted sandbox token.
const pastedRefresh = "quicc-pasted-sandbox-token"

// pastedExpiry: a sandbox personal access token doesn't expire.
const pastedExpiry = 10 * 365 * 24 * time.Hour

var (
	// ErrNotSandbox: pasting a token is only for Square's sandbox.
	ErrNotSandbox = errors.New("pasting a Square token is only allowed on a server that runs Square's sandbox")
	// ErrTokenRefused: Square refused the pasted token.
	ErrTokenRefused = errors.New("that access token was refused by Square: copy the Sandbox access token again")
	// ErrBadLocation: the location isn't one of the token's card-taking locations.
	ErrBadLocation = errors.New("that location id isn't one of this Square account's card-taking locations")
)

// PasteAllowed reports whether this server takes a pasted token (sandbox,
// or the dev fake for the tests; never production).
func (s *Service) PasteAllowed() bool {
	return s.Enabled() && (s.cfg.Env == "sandbox" || s.cfg.Env == "fake")
}

func (s *Service) pasted(c repositories.SquareConnection) bool {
	if s.aead == nil || len(c.RefreshEnc) == 0 {
		return false
	}
	rt, err := s.open(c.RefreshEnc)
	return err == nil && rt == pastedRefresh
}

func (s *Service) pastedLocation(ctx context.Context, locationID uuid.UUID) bool {
	c, err := s.repo.Get(ctx, locationID)
	return err == nil && s.pasted(c)
}

// PasteSandboxToken checks the token with Square (its locations; the
// location must take cards) and stores it as the store's connection.
func (s *Service) PasteSandboxToken(ctx context.Context, locationID uuid.UUID, accessToken, squareLocationID string) error {
	if !s.PasteAllowed() {
		return ErrNotSandbox
	}
	accessToken, squareLocationID = strings.TrimSpace(accessToken), strings.TrimSpace(squareLocationID)
	if accessToken == "" || squareLocationID == "" || len(accessToken) > 512 || len(squareLocationID) > 64 {
		return ErrBadLocation
	}
	loc, err := s.locs.GetLocation(ctx, locationID)
	if err != nil {
		return ErrUnknownPlace
	}
	ls, err := s.client.Locations(ctx, accessToken)
	if err != nil {
		if code := square.StatusOf(err); code == 401 || code == 403 {
			return ErrTokenRefused
		}
		return err
	}
	merchant := ""
	found := false
	for _, l := range ls {
		if l.ID == squareLocationID && l.TakesCards() {
			found, merchant = true, l.MerchantID
		}
	}
	if !found {
		return ErrBadLocation
	}
	if info, err := s.client.TokenStatus(ctx, accessToken); err == nil && info.MerchantID != "" {
		merchant = info.MerchantID
	}
	if merchant == "" {
		merchant = "sandbox-pasted"
	}
	accessEnc, err := s.seal(accessToken)
	if err != nil {
		return err
	}
	refreshEnc, err := s.seal(pastedRefresh)
	if err != nil {
		return err
	}
	if _, err := s.repo.Upsert(ctx, repositories.SquareConnection{LocationID: loc.ID, TenantID: loc.TenantID, MerchantID: merchant,
		SquareLocationID: squareLocationID, AccessEnc: accessEnc, RefreshEnc: refreshEnc, ExpiresAt: s.now().Add(pastedExpiry)}); err != nil {
		return err
	}
	s.grants.mu.Lock()
	delete(s.grants.m, locationID)
	s.grants.mu.Unlock()
	s.logger.Info().Str("location_id", locationID.String()).Str("merchant_id", merchant).Str("square_location_id", squareLocationID).
		Str("env", s.cfg.Env).Msg("square sandbox token pasted") // never the token
	return nil
}
