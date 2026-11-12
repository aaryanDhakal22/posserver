package tenancyApp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	rotated  int
	renamed  int
	retired  bool
}

func (r *credRepo) GetTenantAuthByUsername(_ context.Context, u string) (*tenancy.TenantAuth, error) {
	if u != r.username {
		return nil, pgx.ErrNoRows
	}
	return &tenancy.TenantAuth{TenantID: r.id, PasswordHash: r.hash}, nil
}
func (r *credRepo) SetTenantCredentials(_ context.Context, _ uuid.UUID, _, hash string) error {
	r.hash = hash
	r.rotated++
	return nil
}
func (r *credRepo) TenantLogin(context.Context, uuid.UUID) (string, string, error) {
	return r.username, r.hash, nil
}
func (r *credRepo) SetTenantUsername(_ context.Context, _ uuid.UUID, u string) error {
	r.username = u
	r.renamed++
	return nil
}
func (r *credRepo) RehashTenantPassword(_ context.Context, _ uuid.UUID, hash string) error {
	r.rehashed, r.hash = hash, hash
	return nil
}
func (r *credRepo) GetTenantByID(_ context.Context, id uuid.UUID) (*tenancy.Tenant, error) {
	t := &tenancy.Tenant{ID: id}
	if r.retired {
		now := time.Now()
		t.RetiredAt = &now
	}
	return t, nil
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
	// The upgrade is not a rotation: it must not end the store's sessions.
	if repo.rotated != 0 {
		t.Fatalf("the hash upgrade went through SetTenantCredentials %d time(s): it would log every manager out", repo.rotated)
	}
}

// M8: a retired store's login is refused like a wrong password, also for a
// Pi linking with the manager login (agentsync.Link authenticates here).
func TestAuthenticateTenant_RetiredStoreRefused(t *testing.T) {
	bc, _ := authSvc.HashPassword("pw")
	repo := &credRepo{id: uuid.New(), username: "mgr", hash: bc}
	svc := NewService(repo, zerolog.Nop())
	if _, err := svc.AuthenticateTenant(context.Background(), "mgr", "pw"); err != nil {
		t.Fatalf("live store: %v", err)
	}
	repo.retired = true
	if _, err := svc.AuthenticateTenant(context.Background(), "mgr", "pw"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("retired store: err = %v, want ErrInvalidCredentials", err)
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

// M8: only a new password is a rotation (the repo's SetTenantCredentials adds
// one to the store's session epoch). The same password again, or a rename
// with the same password, leaves the hash and the sessions alone.
func TestSetTenantCredentials_OnlyANewPasswordRotates(t *testing.T) {
	ctx := context.Background()
	repo := &credRepo{id: uuid.New()}
	svc := NewService(repo, zerolog.Nop())
	if err := svc.SetTenantCredentials(ctx, repo.id, "mgr", "pw"); err != nil || repo.rotated != 1 {
		t.Fatalf("first set: err=%v rotated=%d", err, repo.rotated)
	}
	repo.username = "mgr"
	hash := repo.hash
	if err := svc.SetTenantCredentials(ctx, repo.id, "mgr", "pw"); err != nil || repo.rotated != 1 || repo.hash != hash {
		t.Fatalf("the same login again: err=%v rotated=%d (want 1), hash changed=%v", err, repo.rotated, repo.hash != hash)
	}
	if err := svc.SetTenantCredentials(ctx, repo.id, "boss", "pw"); err != nil || repo.rotated != 1 || repo.renamed != 1 || repo.username != "boss" {
		t.Fatalf("rename, same password: err=%v rotated=%d renamed=%d user=%q", err, repo.rotated, repo.renamed, repo.username)
	}
	if err := svc.SetTenantCredentials(ctx, repo.id, "boss", "new-pw"); err != nil || repo.rotated != 2 {
		t.Fatalf("a new password: err=%v rotated=%d (want 2)", err, repo.rotated)
	}
}
