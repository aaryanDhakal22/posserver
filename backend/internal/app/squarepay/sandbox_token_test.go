package squarepay

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"quiccpos/main/internal/infra/database/repositories"
	sseBroker "quiccpos/main/internal/infra/sse"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// deletes is connected + Delete (Disconnect forgets the row).
type deletes struct{ connected }

func (d deletes) Delete(_ context.Context, id uuid.UUID) (bool, error) {
	for i, x := range d.got {
		if x.LocationID == id {
			d.got = append(d.got[:i], d.got[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (d deletes) Get(ctx context.Context, id uuid.UUID) (repositories.SquareConnection, error) {
	for _, x := range d.got {
		if x.LocationID == id {
			return x, nil
		}
	}
	return repositories.SquareConnection{}, pgx.ErrNoRows
}

// A sandbox token pasted in Store › Payments is a connection like OAuth's:
// the reader's session, keyed payments, no refresh, no revoke; never in
// production.
func TestPasteSandboxToken(t *testing.T) {
	s, repo, loc, logs, _ := oauthRig(t)
	ctx := context.Background()
	// A token from the fake Square (another store's OAuth, as the Developer Console's sandbox token).
	code, _ := authorize(t, s, loc.ID)
	tok, err := s.client.ExchangeCode(ctx, code, fakeRedirect)
	if err != nil {
		t.Fatal(err)
	}
	ls, _ := s.client.Locations(ctx, tok.AccessToken)
	sqLoc := ls[0].ID
	d := &deletes{connected{repo}}
	s.repo = d

	s.cfg.Env = "production"
	if err := s.PasteSandboxToken(ctx, loc.ID, tok.AccessToken, sqLoc); !errors.Is(err, ErrNotSandbox) {
		t.Fatalf("production: %v", err)
	}
	s.cfg.Env = "fake"
	if err := s.PasteSandboxToken(ctx, loc.ID, "EAAA-not-a-token", sqLoc); !errors.Is(err, ErrTokenRefused) {
		t.Fatalf("bad token: %v", err)
	}
	if err := s.PasteSandboxToken(ctx, loc.ID, tok.AccessToken, ls[1].ID); !errors.Is(err, ErrBadLocation) { // the inactive one
		t.Fatalf("bad location: %v", err)
	}
	if err := s.PasteSandboxToken(ctx, loc.ID, tok.AccessToken, sqLoc); err != nil {
		t.Fatal(err)
	}
	if len(repo.got) != 1 || repo.got[0].SquareLocationID != sqLoc || repo.got[0].MerchantID != tok.MerchantID {
		t.Fatalf("stored %+v", repo.got)
	}
	repo.got[0].Status = "connected"
	st, err := s.Status(ctx, loc.ID)
	if err != nil || !st.Connected || !st.Pasted || st.InPerson == nil || !*st.InPerson {
		t.Fatalf("status %+v %v", st, err)
	}
	// The terminal's reader session hands it out.
	sc := sseBroker.Scope{TenantID: loc.TenantID, LocationID: loc.ID}
	r, err := call(t, s.HandleSession, sc, map[string]any{})
	if err != nil || !r.OK || r.AccessToken != tok.AccessToken || r.LocationID != sqLoc {
		t.Fatalf("session %+v %v", r, err)
	}
	// Old enough to refresh: a pasted token is used as it is (no refresh call, no error page).
	repo.got[0].RefreshedAt = time.Now().Add(-30 * 24 * time.Hour)
	if got, err := s.accessToken(ctx, repo.got[0]); err != nil || got != tok.AccessToken {
		t.Fatalf("refresh of a pasted token: %q %v", got, err)
	}
	// A keyed payment works with it.
	k, err := call(t, s.HandleKeyedPayment, sc, keyed("cnon:card-nonce-ok", "paste-k1", 500, 0))
	if err != nil || !k.OK {
		t.Fatalf("keyed %+v %v", k, err)
	}
	// Disconnect forgets it without revoking (the token still works at Square).
	if err := s.Disconnect(ctx, loc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.client.Locations(ctx, tok.AccessToken); err != nil {
		t.Fatalf("a pasted token was revoked: %v", err)
	}
	if _, err := s.repo.Get(ctx, loc.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("still connected: %v", err)
	}
	if strings.Contains(logs.String(), tok.AccessToken) {
		t.Fatal("the token is in the log")
	}
}
