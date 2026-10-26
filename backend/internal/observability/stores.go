package observability

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Store labels (observability/TENANT-SCOPING.md §1): every per-order /
// per-store instrument carries `tenant` (the tenant slug) and `location` (the
// location slug). Never a UUID, a name, an order id or customer data — one
// value per store keeps the cardinality low.
const (
	AttrTenant   = "tenant"
	AttrLocation = "location"

	// TenantUnrouted: the measurement happened before the order had a store
	// (cast / scope failure: the online caster is what routes).
	TenantUnrouted = "unrouted"
	// TenantUnknown: a store id was known but its slugs couldn't be loaded
	// (no resolver installed, a deleted tenant, a DB error).
	TenantUnknown = "unknown"
)

// StoreSlugLookup loads (tenant slug, location slug) for a tenant id or a
// location id; exactly one of the two is non-nil. Installed once at bootstrap
// by SetStoreLookup.
type StoreSlugLookup func(ctx context.Context, tenantID, locationID uuid.UUID) (tenant, location string, err error)

const (
	storeLabelTTL    = 10 * time.Minute
	storeLabelErrTTL = 30 * time.Second
	storeLookupLimit = 2 * time.Second
)

type storeKey struct {
	byLocation bool
	id         uuid.UUID
}

type storeEntry struct {
	attrs   []attribute.KeyValue
	expires time.Time
}

// StoreLabels caches id → [tenant, location] attributes so hot paths (order
// ingest, SSE fan-out, RPC) hit the DB at most once per store per TTL.
type StoreLabels struct {
	lookup StoreSlugLookup
	now    func() time.Time
	cache  sync.Map // storeKey → storeEntry
}

// NewStoreLabels builds a resolver. lookup may be nil (every store resolves
// to TenantUnknown), which is what unit tests get.
func NewStoreLabels(lookup StoreSlugLookup) *StoreLabels {
	return &StoreLabels{lookup: lookup, now: time.Now}
}

var storeLabels atomic.Pointer[StoreLabels]

// SetStoreLookup installs the process-wide resolver used by StoreAttrs /
// LocationAttrs. Call once during bootstrap, before traffic.
func SetStoreLookup(lookup StoreSlugLookup) { storeLabels.Store(NewStoreLabels(lookup)) }

func pair(tenant, location string) []attribute.KeyValue {
	return []attribute.KeyValue{attribute.String(AttrTenant, tenant), attribute.String(AttrLocation, location)}
}

var (
	unroutedAttrs = pair(TenantUnrouted, TenantUnrouted)
	unknownAttrs  = pair(TenantUnknown, TenantUnknown)
)

// UnroutedAttrs is the store label pair for a measurement with no store.
func UnroutedAttrs() []attribute.KeyValue { return unroutedAttrs }

// StoreAttrs returns the [tenant, location] attributes for a tenant id
// (TenantUnrouted for uuid.Nil).
func StoreAttrs(ctx context.Context, tenantID uuid.UUID) []attribute.KeyValue {
	return resolve(ctx, storeKey{id: tenantID})
}

// LocationAttrs is StoreAttrs keyed by a location id.
func LocationAttrs(ctx context.Context, locationID uuid.UUID) []attribute.KeyValue {
	return resolve(ctx, storeKey{byLocation: true, id: locationID})
}

// WithStore is metric.WithAttributes(tenant, location, extra...) for a tenant id.
func WithStore(ctx context.Context, tenantID uuid.UUID, extra ...attribute.KeyValue) metric.MeasurementOption {
	return metric.WithAttributes(withExtra(StoreAttrs(ctx, tenantID), extra)...)
}

// WithLocation is WithStore keyed by a location id.
func WithLocation(ctx context.Context, locationID uuid.UUID, extra ...attribute.KeyValue) metric.MeasurementOption {
	return metric.WithAttributes(withExtra(LocationAttrs(ctx, locationID), extra)...)
}

// WithUnrouted is metric.WithAttributes(tenant=unrouted, location=unrouted, extra...).
func WithUnrouted(extra ...attribute.KeyValue) metric.MeasurementOption {
	return metric.WithAttributes(withExtra(unroutedAttrs, extra)...)
}

func withExtra(store, extra []attribute.KeyValue) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(store)+len(extra))
	return append(append(out, store...), extra...)
}

func resolve(ctx context.Context, k storeKey) []attribute.KeyValue {
	if k.id == uuid.Nil {
		return unroutedAttrs
	}
	s := storeLabels.Load()
	if s == nil {
		return unknownAttrs
	}
	return s.attrs(ctx, k)
}

// Attrs resolves one key through this resolver (exported for tests).
func (s *StoreLabels) Attrs(ctx context.Context, tenantID, locationID uuid.UUID) []attribute.KeyValue {
	switch {
	case tenantID != uuid.Nil:
		return s.attrs(ctx, storeKey{id: tenantID})
	case locationID != uuid.Nil:
		return s.attrs(ctx, storeKey{byLocation: true, id: locationID})
	}
	return unroutedAttrs
}

func (s *StoreLabels) attrs(ctx context.Context, k storeKey) []attribute.KeyValue {
	now := s.now()
	if v, ok := s.cache.Load(k); ok {
		if e := v.(storeEntry); now.Before(e.expires) {
			return e.attrs
		}
	}
	if s.lookup == nil {
		return unknownAttrs
	}
	// Detached from the caller's cancellation (a finished request must not
	// turn into "unknown"), but bounded; trace context is kept.
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), storeLookupLimit)
	defer cancel()
	var tid, lid uuid.UUID
	if k.byLocation {
		lid = k.id
	} else {
		tid = k.id
	}
	tenant, location, err := s.lookup(lctx, tid, lid)
	e := storeEntry{attrs: pair(tenant, location), expires: now.Add(storeLabelTTL)}
	if err != nil || tenant == "" {
		e = storeEntry{attrs: unknownAttrs, expires: now.Add(storeLabelErrTTL)}
	} else if location == "" {
		e.attrs = pair(tenant, TenantUnknown)
	}
	s.cache.Store(k, e)
	return e.attrs
}
