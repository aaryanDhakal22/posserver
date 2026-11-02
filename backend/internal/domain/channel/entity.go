// Package channel models a location's sales channels (main 3.0): every way an
// order can reach a store — the web storefront, the call center, a counter
// POS terminal, a vendor feed — is a channel that talks to main's /channel/v1
// ports with its own bearer key. A channel holds no business rules; main owns
// the menu, prices, store status, orders, payments and tracking behind the
// ports, and the channel's kind + permissions decide what its key may do.
package channel

import (
	"time"

	"github.com/google/uuid"
)

// Kind is what sort of channel this is.
type Kind string

const (
	KindWeb      Kind = "web"      // the customer storefront
	KindOperator Kind = "operator" // the call center
	KindCounter  Kind = "counter"  // a POS terminal in the store
	KindVendor   Kind = "vendor"   // a marketplace / vendor feed (Brygid today)
)

// Kinds lists every kind, in display order.
var Kinds = []Kind{KindWeb, KindOperator, KindCounter, KindVendor}

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	for _, x := range Kinds {
		if x == k {
			return true
		}
	}
	return false
}

// Payment methods a channel may take an order with.
const (
	PayCard = "card"
	PayCash = "cash"
)

// Permissions is what a channel's key may do. Menu / Quote / Orders / Track
// gate the ports; Payments lists the methods an order through it may use
// (cash = pay at the store or to the driver, card = paid through the payment
// port).
type Permissions struct {
	Menu     bool     `json:"menu"`
	Quote    bool     `json:"quote"`
	Orders   bool     `json:"orders"`
	Track    bool     `json:"track"`
	Payments []string `json:"payments"`
}

// DefaultPermissions are a new channel's permissions by kind: the storefront
// takes card only, the call center and the counter card or cash, a vendor
// feed only places orders (it took the payment itself).
func DefaultPermissions(k Kind) Permissions {
	switch k {
	case KindWeb:
		return Permissions{Menu: true, Quote: true, Orders: true, Track: true, Payments: []string{PayCard}}
	case KindOperator, KindCounter:
		return Permissions{Menu: true, Quote: true, Orders: true, Track: true, Payments: []string{PayCard, PayCash}}
	case KindVendor:
		return Permissions{Orders: true, Track: true, Payments: []string{PayCard, PayCash}}
	}
	return Permissions{Payments: []string{}}
}

// Channel is one sales channel of a location.
type Channel struct {
	ID          uuid.UUID   `json:"id"`
	TenantID    uuid.UUID   `json:"tenant_id"`
	LocationID  uuid.UUID   `json:"location_id"`
	Kind        Kind        `json:"kind"`
	Name        string      `json:"name"`
	KeyPrefix   string      `json:"key_prefix"`
	Permissions Permissions `json:"permissions"`
	Builtin     bool        `json:"builtin"`
	// ViewID is the menu view this channel reads (nil = its store's default
	// view of its kind).
	ViewID     *uuid.UUID `json:"view_id"`
	RevokedAt  *time.Time `json:"revoked_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Active reports whether the channel's key works.
func (c Channel) Active() bool { return c.RevokedAt == nil && c.KeyPrefix != "" }
