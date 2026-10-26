package order

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// OrderFilter narrows an order-history query. Zero values mean "no filter":
// empty Query/Status match everything, nil From/To leave that bound open. When
// Reverse is true results come back oldest-first instead of newest-first.
type OrderFilter struct {
	Query   string
	Status  string
	From    *time.Time
	To      *time.Time
	Reverse bool
	Offset  int
	Limit   int
}

// Scope is the (tenant_id, location_id) pair every authenticated request
// resolves to. Read endpoints supply the caller's scope; the repo applies it
// as a WHERE clause so a leaked id cannot be used to fetch another tenant's
// data.
type Scope struct {
	TenantID   TenantID
	LocationID LocationID
}

// CastInput is the input contract every per-source caster accepts. Defined
// here (vs. in the consumer) so casters can implement the interface without
// importing transport packages — the casting layer stays pure domain logic.
type CastInput struct {
	Raw        []byte
	TenantID   TenantID
	LocationID LocationID
}

// Summary is a lightweight, cross-tenant view of an order for the admin surface
// (no children assembled). IDs are strings — this only ever goes to JSON.
type Summary struct {
	ID            string
	TenantID      string
	LocationID    string
	Source        string
	SourceEventID string
	Status        string
	CustomerName  string
	TotalCents    int64
	ReceivedAt    time.Time
}

// ListFilter narrows the admin cross-tenant order listing. Empty TenantID /
// SourceEventID mean "any"; Limit ≤ 0 falls back to a default.
type ListFilter struct {
	TenantID      string
	SourceEventID string
	Limit         int
}

// OrderRepository persists and retrieves the canonical Order aggregate. All
// reads are scope-checked.
type OrderRepository interface {
	Create(ctx context.Context, o *Order) error
	GetByID(ctx context.Context, scope Scope, id ID) (*Order, error)
	FindBySourceEvent(ctx context.Context, scope Scope, src Source, sourceEventID string) (*Order, error)
	GetOrdersPage(ctx context.Context, scope Scope, offset, limit int) ([]*Order, error)
	// SearchOrders is GetOrdersPage plus optional text/status/date filters and
	// sort direction — backs the agent's store-manager history view.
	SearchOrders(ctx context.Context, scope Scope, f OrderFilter) ([]*Order, error)
	// DeleteByID removes a single order and its children (line_items, payments,
	// status_events cascade; outbox rows removed explicitly). Admin-only.
	DeleteByID(ctx context.Context, id ID) error
	// ListSummaries returns a cross-tenant, unscoped order listing (admin-only).
	ListSummaries(ctx context.Context, f ListFilter) ([]Summary, error)
	// DeleteAll bulk-deletes orders + their outbox rows. Empty tenantID = every
	// order; a non-empty tenantID scopes the delete to one tenant. Admin-only.
	// Returns the number of orders deleted.
	DeleteAll(ctx context.Context, tenantID string) (int64, error)
}

// Driver is one delivery driver as the location's agent last reported it.
type Driver struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Position  int       `json:"position"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DriverShift is one driver clock-in → clock-out, as the agent reported it.
type DriverShift struct {
	ID         uuid.UUID  `json:"id"`
	DriverID   uuid.UUID  `json:"driver_id"`
	DriverName string     `json:"driver_name"`
	ClockIn    time.Time  `json:"clock_in"`
	ClockOut   *time.Time `json:"clock_out,omitempty"`
	AutoOut    bool       `json:"auto_out"`
}

// FloorRepository persists the agent's floor events (see floor.go).
type FloorRepository interface {
	// ApplyFloorUpdate locks the order (in scope), runs apply on it, records
	// eventID as seen and — when apply returns FloorApplied — persists the
	// floor changes (status + fields, new status_events, outbox events), all in
	// one transaction. FloorDuplicate if eventID was already seen; the error
	// wraps pgx.ErrNoRows when the order isn't in scope.
	ApplyFloorUpdate(ctx context.Context, scope Scope, id ID, eventID uuid.UUID, typ string, apply func(o *Order) string) (string, error)
	// MarkFloorEventSeen records an event that changed nothing (not_found /
	// invalid); dup=true if it was already recorded.
	MarkFloorEventSeen(ctx context.Context, scope Scope, eventID uuid.UUID, orderID *uuid.UUID, typ, result string) (dup bool, err error)
	// ReplaceDrivers mirrors the agent's whole roster (idempotent on eventID).
	ReplaceDrivers(ctx context.Context, scope Scope, eventID uuid.UUID, drivers []Driver) (string, error)
	ListDrivers(ctx context.Context, scope Scope) ([]Driver, error)
	// SaveDriverShift upserts a shift (idempotent on eventID).
	SaveDriverShift(ctx context.Context, scope Scope, eventID uuid.UUID, sh DriverShift) (string, error)
	ListDriverShifts(ctx context.Context, scope Scope, since time.Time) ([]DriverShift, error)
	// LastFloorEventAt is when the location's agent last synced a floor event.
	LastFloorEventAt(ctx context.Context, scope Scope) (*time.Time, error)
	// SetAgentStatus stores the agent's full floor status for an order (and
	// the agent batch it belongs to) — what a replacement agent restores
	// from. Ignored when an equal-or-newer status is already stored. Also
	// marks the order acknowledged by the agent.
	SetAgentStatus(ctx context.Context, scope Scope, id uuid.UUID, status []byte, batchID *int64, at time.Time) error
}
