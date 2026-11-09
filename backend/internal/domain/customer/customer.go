// Package customer models a store's customer accounts (weborder stage 2):
// an account belongs to one store, is keyed by its verified phone, keeps a
// name + email, saved addresses and saved cards (on the card processor —
// only its id, brand and last 4 here), and links the store's web orders.
package customer

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// SessionPrefix starts every customer session token.
const SessionPrefix = "qcs_"

// SessionFor is how long a session lives; every use renews it (owner, 2026-11-09).
const SessionFor = 90 * 24 * time.Hour

var (
	ErrNotFound    = errors.New("not found")
	ErrBadSession  = errors.New("sign in again")
	ErrBadPhone    = errors.New("phone must be a 10-digit US number")
	ErrBadCode     = errors.New("that code isn't right — check it or ask for a new one")
	ErrTooMany     = errors.New("too many tries — wait a little and try again")
	ErrCallTooSoon = errors.New("wait a few seconds for the text before asking for a call")
	ErrBadAddress  = errors.New("an address needs line1 and a zip or city")
	ErrBadEmail    = errors.New("that isn't an email address")
	ErrTooLong     = errors.New("too long")
	ErrMaxAddress  = errors.New("an account keeps up to 20 addresses")
)

// Customer is one store's customer account.
type Customer struct {
	ID               uuid.UUID `json:"id"`
	TenantID         uuid.UUID `json:"-"`
	LocationID       uuid.UUID `json:"-"`
	Phone            string    `json:"phone"`
	Name             string    `json:"name"`
	Email            string    `json:"email"`
	SquareCustomerID string    `json:"-"`
	CreatedAt        time.Time `json:"created_at"`
}

// Address is a saved delivery address.
type Address struct {
	ID        uuid.UUID `json:"id"`
	Label     string    `json:"label"`
	Line1     string    `json:"line1"`
	Line2     string    `json:"line2"`
	City      string    `json:"city"`
	State     string    `json:"state"`
	Zip       string    `json:"zip"`
	Notes     string    `json:"notes"`
	Default   bool      `json:"default"`
	CreatedAt time.Time `json:"created_at"`
}

// Card is a card saved on the processor (Square) for one-tap checkout.
type Card struct {
	ID        uuid.UUID `json:"id"`
	Gateway   string    `json:"-"`
	Ref       string    `json:"-"` // the processor's card id
	Brand     string    `json:"brand"`
	Last4     string    `json:"last4"`
	ExpMonth  int       `json:"exp_month"`
	ExpYear   int       `json:"exp_year"`
	CreatedAt time.Time `json:"created_at"`
}

// E164 is a 10-digit US number as +1XXXXXXXXXX.
func E164(phone10 string) string { return "+1" + phone10 }
