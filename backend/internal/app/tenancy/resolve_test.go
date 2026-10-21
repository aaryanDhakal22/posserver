package tenancyApp

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"quiccpos/main/internal/domain/tenancy"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// fakeRepo implements only what ResolveStore touches; any other method hits
// the nil embedded interface and panics, flagging an unexpected call.
type fakeRepo struct {
	tenancy.Repository
	tenants   []tenancy.Tenant
	locations map[uuid.UUID][]tenancy.Location
}

func (f *fakeRepo) GetTenantByStoreID(_ context.Context, storeID int64) (*tenancy.Tenant, error) {
	for i := range f.tenants {
		if f.tenants[i].StoreID != nil && *f.tenants[i].StoreID == storeID {
			return &f.tenants[i], nil
		}
	}
	return nil, fmt.Errorf("get tenant by store_id: %w", pgx.ErrNoRows)
}

func (f *fakeRepo) ListTenants(context.Context) ([]tenancy.Tenant, error) {
	return f.tenants, nil
}

func (f *fakeRepo) ListLocationsByTenant(_ context.Context, tenantID uuid.UUID) ([]tenancy.Location, error) {
	return f.locations[tenantID], nil
}

func i64(v int64) *int64 { return &v }

func TestResolveStore(t *testing.T) {
	lombardis := tenancy.Tenant{ID: uuid.New(), Name: "Lombardi's Pizza", StoreID: i64(3282)}
	dragon := tenancy.Tenant{ID: uuid.New(), Name: "Golden Dragon"}               // no store_id
	noLoc := tenancy.Tenant{ID: uuid.New(), Name: "Empty Shell", StoreID: i64(7)} // mapped, no location
	twinA := tenancy.Tenant{ID: uuid.New(), Name: "Lombardi's Express"}
	lomLoc := tenancy.Location{ID: uuid.New(), TenantID: lombardis.ID}
	dragonLoc := tenancy.Location{ID: uuid.New(), TenantID: dragon.ID}

	base := func() *fakeRepo {
		return &fakeRepo{
			tenants: []tenancy.Tenant{lombardis, dragon, noLoc},
			locations: map[uuid.UUID][]tenancy.Location{
				lombardis.ID: {lomLoc},
				dragon.ID:    {dragonLoc},
			},
		}
	}

	tests := []struct {
		name       string
		repo       *fakeRepo
		storeID    int64
		storeName  string
		wantTenant uuid.UUID
		wantLoc    uuid.UUID
		wantMethod ResolveMethod
		wantErr    error
	}{
		{
			name: "mapped store_id wins even with a mismatched name", repo: base(),
			storeID: 3282, storeName: "Totally Different",
			wantTenant: lombardis.ID, wantLoc: lomLoc.ID, wantMethod: ResolvedByStoreID,
		},
		{
			name: "unmapped store_id falls back to name", repo: base(),
			storeID: 9999, storeName: "Golden Dragon",
			wantTenant: dragon.ID, wantLoc: dragonLoc.ID, wantMethod: ResolvedByNameFallback,
		},
		{
			name: "absent store_id (0) falls back to name", repo: base(),
			storeID: 0, storeName: "Lombardi's Pizza",
			wantTenant: lombardis.ID, wantLoc: lomLoc.ID, wantMethod: ResolvedByNameFallback,
		},
		{
			name: "unmapped store_id + unknown name is unresolved", repo: base(),
			storeID: 9999, storeName: "Nobody Here",
			wantMethod: ResolvedByNameFallback, wantErr: ErrStoreUnresolved,
		},
		{
			name: "mapped store_id with no location is unresolved (no fallback)", repo: base(),
			storeID: 7, storeName: "Golden Dragon",
			wantMethod: ResolvedByStoreID, wantErr: ErrStoreUnresolved,
		},
		{
			name:    "ambiguous name prefix is unresolved",
			repo:    func() *fakeRepo { r := base(); r.tenants = append(r.tenants, twinA); return r }(),
			storeID: 0, storeName: "Lombardi's",
			wantMethod: ResolvedByNameFallback, wantErr: ErrStoreUnresolved,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(tc.repo, zerolog.Nop())
			tid, lid, method, err := svc.ResolveStore(context.Background(), tc.storeID, tc.storeName)
			if method != tc.wantMethod {
				t.Errorf("method = %q, want %q", method, tc.wantMethod)
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if tid != tc.wantTenant || lid != tc.wantLoc {
				t.Fatalf("got %v/%v, want %v/%v", tid, lid, tc.wantTenant, tc.wantLoc)
			}
		})
	}
}

func TestStoreIDValidation(t *testing.T) {
	svc := NewService(&fakeRepo{}, zerolog.Nop())
	for _, bad := range []int64{0, -5} {
		if _, err := svc.SetTenantStoreID(context.Background(), uuid.New(), i64(bad)); !errors.Is(err, ErrInvalidStoreID) {
			t.Errorf("store_id %d: err = %v, want ErrInvalidStoreID", bad, err)
		}
		if _, _, err := svc.CreateTenant(context.Background(), "s", "n", "", "", i64(bad)); !errors.Is(err, ErrInvalidStoreID) {
			t.Errorf("create with store_id %d: err = %v, want ErrInvalidStoreID", bad, err)
		}
	}
}
