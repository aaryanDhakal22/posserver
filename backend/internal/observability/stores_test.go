package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
)

func labels(kv []attribute.KeyValue) (tenant, location string) {
	for _, a := range kv {
		switch a.Key {
		case AttrTenant:
			tenant = a.Value.AsString()
		case AttrLocation:
			location = a.Value.AsString()
		}
	}
	return
}

func TestStoreLabelsCachesAndExpires(t *testing.T) {
	tid, lid := uuid.New(), uuid.New()
	calls := 0
	s := NewStoreLabels(func(_ context.Context, tenantID, locationID uuid.UUID) (string, string, error) {
		calls++
		if tenantID == tid || locationID == lid {
			return "lombardis", "main", nil
		}
		return "", "", errors.New("no rows")
	})
	now := time.Unix(1_000_000, 0)
	s.now = func() time.Time { return now }
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if te, lo := labels(s.Attrs(ctx, tid, uuid.Nil)); te != "lombardis" || lo != "main" {
			t.Fatalf("got %q/%q", te, lo)
		}
	}
	if calls != 1 {
		t.Fatalf("lookup called %d times, want 1 (cached)", calls)
	}
	if te, _ := labels(s.Attrs(ctx, uuid.Nil, lid)); te != "lombardis" {
		t.Fatalf("by location: got %q", te)
	}
	if calls != 2 {
		t.Fatalf("location key is cached separately: %d calls", calls)
	}

	now = now.Add(storeLabelTTL + time.Second)
	s.Attrs(ctx, tid, uuid.Nil)
	if calls != 3 {
		t.Fatalf("expired entry not reloaded: %d calls", calls)
	}

	// Unknown store: "unknown", cached only briefly.
	other := uuid.New()
	if te, lo := labels(s.Attrs(ctx, other, uuid.Nil)); te != TenantUnknown || lo != TenantUnknown {
		t.Fatalf("lookup error: got %q/%q", te, lo)
	}
	s.Attrs(ctx, other, uuid.Nil)
	if calls != 4 {
		t.Fatalf("error result not cached: %d calls", calls)
	}
	now = now.Add(storeLabelErrTTL + time.Second)
	s.Attrs(ctx, other, uuid.Nil)
	if calls != 5 {
		t.Fatalf("error result cached too long: %d calls", calls)
	}

	if te, lo := labels(s.Attrs(ctx, uuid.Nil, uuid.Nil)); te != TenantUnrouted || lo != TenantUnrouted {
		t.Fatalf("no store: got %q/%q", te, lo)
	}
}

func TestStoreAttrsWithoutResolver(t *testing.T) {
	prev := storeLabels.Swap(nil)
	t.Cleanup(func() { storeLabels.Store(prev) })
	ctx := context.Background()
	if te, _ := labels(StoreAttrs(ctx, uuid.New())); te != TenantUnknown {
		t.Fatalf("no resolver: got %q", te)
	}
	if te, _ := labels(StoreAttrs(ctx, uuid.Nil)); te != TenantUnrouted {
		t.Fatalf("nil tenant: got %q", te)
	}
}
