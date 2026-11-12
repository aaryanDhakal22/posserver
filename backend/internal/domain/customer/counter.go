package customer

import (
	"context"

	"github.com/google/uuid"
)

// Customers made or changed at the counter (TERMINAL-PLAN 1d): the Pi saves
// them at once (offline-safe) and sends each change to main as the floor
// event `customer.upsert` (contract/main.md §2.5zc). main applies it with
// the import's care: a name or notes the website wrote are never
// overwritten; the counter may overwrite what it or an import wrote.

// CounterMayWrite reports whether the counter may overwrite a name or notes
// last written by source: anything but the website's (an empty field always).
func CounterMayWrite(source, current string) bool {
	return current == "" || source != SourceWeb
}

// CounterTx is the counter upsert's view of one store's customers inside
// one transaction (repositories.CustomerRepository opens it).
type CounterTx interface {
	// ByID is the store's customer with this id (ok false: none in this store).
	ByID(ctx context.Context, locationID, id uuid.UUID) (Customer, []Address, bool, error)
	// ByPhone is the store's customer with this phone.
	ByPhone(ctx context.Context, locationID uuid.UUID, phone string) (Customer, []Address, bool, error)
	// CreateCounter inserts a customer made at the counter under c.ID (source counter).
	CreateCounter(ctx context.Context, tenantID, locationID uuid.UUID, c Customer) (Customer, error)
	// SetNamesNotes writes name / first / last / notes and their sources.
	SetNamesNotes(ctx context.Context, c Customer) error
	// AddAddress adds an address under a.ID (a new id when that one is taken).
	AddAddress(ctx context.Context, customerID uuid.UUID, a Address) (Address, error)
	// UpdateAddress rewrites an address's fields (not its default flag).
	UpdateAddress(ctx context.Context, customerID uuid.UUID, a Address) error
	// FillAddress rewrites an address's label, line2, city, state, zip and notes.
	FillAddress(ctx context.Context, a Address) error
}
