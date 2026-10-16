package tenancyApp

import (
	"context"
	"errors"
	"strings"
	"testing"

	authSvc "quiccpos/main/internal/app/auth"
	"quiccpos/main/internal/domain/tenancy"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// credRepo fakes just the login-related repo calls.
type credRepo struct {
	tenancy.Repository
	id       uuid.UUID
	username string
	hash     string
	rehashed string
}

func (r *credRepo) GetTenantAuthByUsername(_ context.Context, u string) (*tenancy.TenantAuth, error) {
	if u != r.username {
		return nil, pgx.ErrNoRows
	}
	return &tenancy.TenantAuth{TenantID: r.id, PasswordHash: r.hash}, nil
}
func (r *credRepo) SetTenantCredentials(_ context.Context, _ uuid.UUID, _, hash string) error {
	r.rehashed, r.hash = hash, hash
	return nil
}
func (r *credRepo) GetTenantByID(_ context.Context, id uuid.UUID) (*tenancy.Tenant, error) {
	return &tenancy.Tenant{ID: id}, nil
}

func TestAuthenticateTenant_LegacyHashUpgradesToBcrypt(t *testing.T) {
	repo := &credRepo{id: uuid.New(), username: "mgr", hash: authSvc.HashKey("pw")}
	svc := NewService(repo, zerolog.Nop())

	if _, err := svc.AuthenticateTenant(context.Background(), "mgr", "pw"); err != nil {
		t.Fatalf("legacy login should succeed: %v", err)
	}
	if !strings.HasPrefix(repo.rehashed, "$2") {
		t.Fatalf("legacy hash should be re-saved as bcrypt, got %q", repo.rehashed)
	}
	// The upgraded hash keeps working, and is not re-saved again.
	repo.rehashed = ""
	if _, err := svc.AuthenticateTenant(context.Background(), "mgr", "pw"); err != nil {
		t.Fatalf("bcrypt login should succeed: %v", err)
	}
	if repo.rehashed != "" {
		t.Fatal("a bcrypt hash must not be rehashed on login")
	}
}

func TestAuthenticateTenant_WrongPasswordAndUnknownUser(t *testing.T) {
	bc, _ := authSvc.HashPassword("pw")
	svc := NewService(&credRepo{id: uuid.New(), username: "mgr", hash: bc}, zerolog.Nop())
	for _, tc := range []struct{ u, p string }{{"mgr", "nope"}, {"ghost", "pw"}, {"mgr", ""}} {
		if _, err := svc.AuthenticateTenant(context.Background(), tc.u, tc.p); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("(%q,%q): err = %v, want ErrInvalidCredentials", tc.u, tc.p, err)
		}
	}
}

func TestSetTenantCredentials_StoresBcrypt(t *testing.T) {
	repo := &credRepo{id: uuid.New(), username: "mgr"}
	svc := NewService(repo, zerolog.Nop())
	if err := svc.SetTenantCredentials(context.Background(), repo.id, "mgr", "pw"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(repo.hash, "$2") {
		t.Fatalf("new credentials must be bcrypt, got %q", repo.hash)
	}
}
