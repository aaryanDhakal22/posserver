package customer

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// MaxAddresses is how many addresses an account keeps.
const MaxAddresses = 20

// ImportTx is the bulk import's view of one store's customers inside one
// transaction (repositories.CustomerRepository opens it; a dry run rolls it
// back).
type ImportTx interface {
	// ByPhones are the store's customers with these phones, with their addresses.
	ByPhones(ctx context.Context, locationID uuid.UUID, phones []string) (map[string]Customer, map[uuid.UUID][]Address, error)
	// Create inserts an imported customer (source import). ErrPhoneTaken: a
	// customer with that phone exists after all (another writer made it after
	// ByPhones read: a website sign-in; the Pi's counter writes wait for the
	// import's store lock).
	Create(ctx context.Context, tenantID, locationID uuid.UUID, c Customer) (Customer, error)
	// SetNamesNotes writes name / first / last / notes and their sources.
	SetNamesNotes(ctx context.Context, c Customer) error
	AddAddress(ctx context.Context, customerID uuid.UUID, a Address) (Address, error)
	// FillAddress rewrites an address's label, line2, city, state, zip and notes.
	FillAddress(ctx context.Context, a Address) error
}

// ErrPhoneTaken: ImportTx.Create found the store already has a customer
// with that phone (the import then merges into it, as with one it read).
var ErrPhoneTaken = errors.New("a customer with that phone exists")

// Change is one row of the Pi's change feed: a customer changed (or was
// deleted) at At.
type Change struct {
	ID      uuid.UUID
	At      time.Time
	Deleted bool
}
