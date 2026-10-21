package tenancyApp

import (
	"context"
	"errors"
	"testing"

	"quiccpos/main/internal/domain/tenancy"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// usernameRepo records whether a write reached the repo; any other method hits
// the nil embedded interface and panics.
type usernameRepo struct {
	tenancy.Repository
	created, credsSet bool
	err               error // returned by the writes (e.g. ErrUsernameTaken)
}

func (r *usernameRepo) CreateTenant(_ context.Context, p tenancy.CreateTenantParams) (*tenancy.Tenant, *tenancy.Location, error) {
	r.created = true
	if r.err != nil {
		return nil, nil, r.err
	}
	t := &tenancy.Tenant{ID: uuid.New(), Slug: p.Slug, Name: p.Name, Username: p.Username}
	return t, &tenancy.Location{ID: uuid.New(), TenantID: t.ID}, nil
}

func (r *usernameRepo) SetTenantCredentials(context.Context, uuid.UUID, string, string) error {
	r.credsSet = true
	return r.err
}

func TestReservedUsernameRejected(t *testing.T) {
	ctx := context.Background()
	for _, u := range []string{"admin", "Admin", "  ADMIN  ", "aDmIn"} {
		repo := &usernameRepo{}
		svc := NewService(repo, zerolog.Nop())
		if _, _, err := svc.CreateTenant(ctx, "s", "S", u, "pw", nil); !errors.Is(err, ErrReservedUsername) {
			t.Fatalf("CreateTenant(%q): err = %v, want ErrReservedUsername", u, err)
		}
		if err := svc.SetTenantCredentials(ctx, uuid.New(), u, "pw"); !errors.Is(err, ErrReservedUsername) {
			t.Fatalf("SetTenantCredentials(%q): err = %v, want ErrReservedUsername", u, err)
		}
		if repo.created || repo.credsSet {
			t.Fatalf("%q: a reserved username reached the repo", u)
		}
	}
}

func TestNonReservedUsernamesAllowed(t *testing.T) {
	ctx := context.Background()
	for _, u := range []string{"administrator", "admin2", "lombardispizza", ""} {
		repo := &usernameRepo{}
		svc := NewService(repo, zerolog.Nop())
		pw := "pw"
		if u == "" {
			pw = "" // no login at all
		}
		if _, _, err := svc.CreateTenant(ctx, "s", "S", u, pw, nil); err != nil {
			t.Fatalf("CreateTenant(%q): %v", u, err)
		}
		if u == "" {
			continue
		}
		if err := svc.SetTenantCredentials(ctx, uuid.New(), u, "pw"); err != nil {
			t.Fatalf("SetTenantCredentials(%q): %v", u, err)
		}
	}
}

func TestUsernameTakenPassesThrough(t *testing.T) {
	ctx := context.Background()
	svc := NewService(&usernameRepo{err: tenancy.ErrUsernameTaken}, zerolog.Nop())
	if _, _, err := svc.CreateTenant(ctx, "s", "S", "mgr", "pw", nil); !errors.Is(err, tenancy.ErrUsernameTaken) {
		t.Fatalf("CreateTenant: err = %v, want ErrUsernameTaken", err)
	}
	if err := svc.SetTenantCredentials(ctx, uuid.New(), "mgr", "pw"); !errors.Is(err, tenancy.ErrUsernameTaken) {
		t.Fatalf("SetTenantCredentials: err = %v, want ErrUsernameTaken", err)
	}
}
