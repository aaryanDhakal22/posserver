package customers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"quiccpos/main/internal/domain/customer"

	"github.com/google/uuid"
)

// Terminal 1d: a customer made or changed at the counter, the Pi's floor
// event `customer.upsert` (contract/main.md §2.5zc).

// CounterAddress is one address the counter added or changed.
type CounterAddress struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Line1   string `json:"line1"`
	Line2   string `json:"line2"`
	City    string `json:"city"`
	State   string `json:"state"`
	Zip     string `json:"zip"`
	Notes   string `json:"notes"`
	Default bool   `json:"default"`
}

// CounterCustomer is the customer as the Pi has it: its names and notes,
// and only the addresses this change touched.
type CounterCustomer struct {
	ID        string           `json:"id"`
	Phone     string           `json:"phone"`
	FirstName string           `json:"first_name"`
	LastName  string           `json:"last_name"`
	Notes     string           `json:"notes"`
	Addresses []CounterAddress `json:"addresses"`
}

// Fields a customer.upsert may set (its `set` list).
const (
	SetName  = "name"
	SetNotes = "notes"
)

// ErrBadCounterCustomer: a customer.upsert main can't take (answered invalid).
var ErrBadCounterCustomer = errors.New("bad counter customer")

func badCounter(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadCounterCustomer, fmt.Sprintf(format, a...))
}

// CheckedCounter is a checked customer.upsert.
type CheckedCounter struct {
	id        uuid.UUID
	phone     string
	first     string
	last      string
	notes     string
	setName   bool
	setNotes  bool
	addresses []customer.Address // ID = the Pi's id (uuid.Nil when it sent none)
}

// CheckCounter validates a customer.upsert's customer + set list.
func CheckCounter(raw json.RawMessage, set []string) (CheckedCounter, error) {
	var in CounterCustomer
	if len(raw) == 0 || string(raw) == "null" {
		return CheckedCounter{}, badCounter("no customer")
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return CheckedCounter{}, badCounter("customer: %v", err)
	}
	id, err := uuid.Parse(in.ID)
	if err != nil {
		return CheckedCounter{}, badCounter("customer id is not a uuid")
	}
	phone := NormPhone(in.Phone)
	if phone == "" {
		return CheckedCounter{}, badCounter("phone must be a 10-digit US number")
	}
	t := func(s string, n int) string { return clip(strings.Join(strings.Fields(s), " "), n) }
	out := CheckedCounter{id: id, phone: phone, first: t(in.FirstName, 60), last: t(in.LastName, 60),
		notes: clip(strings.TrimSpace(in.Notes), 1000)}
	for _, f := range set {
		switch f {
		case SetName:
			out.setName = true
		case SetNotes:
			out.setNotes = true
		default:
			return CheckedCounter{}, badCounter("set: %q (name, notes)", f)
		}
	}
	if len(in.Addresses) > customer.MaxAddresses {
		return CheckedCounter{}, badCounter("at most %d addresses", customer.MaxAddresses)
	}
	for _, a := range in.Addresses {
		ca, why := cleanImportAddress(ImportAddress{Label: a.Label, Line1: a.Line1, Line2: a.Line2, City: a.City, State: a.State,
			Zip: a.Zip, Notes: a.Notes})
		if why != "" {
			return CheckedCounter{}, badCounter("address: %s", why)
		}
		if a.ID != "" {
			aid, err := uuid.Parse(a.ID)
			if err != nil {
				return CheckedCounter{}, badCounter("address id is not a uuid")
			}
			ca.ID = aid
		}
		ca.Default = a.Default
		out.addresses = append(out.addresses, ca)
	}
	return out, nil
}

// Floor results (the same words as order.Floor*).
const (
	resultApplied = "applied"
	resultInvalid = "invalid"
)

// ApplyCounter applies a checked customer.upsert to the store: the customer
// with the Pi's id, else the one with its phone (the Pi's copy was behind),
// else a new one under the Pi's id (source counter). Names and notes are
// written only when listed in `set` and the counter may (never over the
// website's, never blanking a name, clearing notes only the counter wrote);
// an address with a known id is the counter's edit of it, else the same
// place gets its blanks filled, else it is added (≤ 20).
func (s *Service) ApplyCounter(ctx context.Context, tenantID, locationID, eventID uuid.UUID, typ string,
	u CheckedCounter) (string, error) {
	created := false
	res, err := s.repo.CounterUpsert(ctx, tenantID, locationID, eventID, typ, func(tx customer.CounterTx) (string, error) {
		c, have, ok, err := tx.ByID(ctx, locationID, u.id)
		if err != nil {
			return "", err
		}
		if ok && c.Phone != u.phone {
			return resultInvalid, nil // the counter never changes a customer's phone
		}
		if !ok {
			if c, have, ok, err = tx.ByPhone(ctx, locationID, u.phone); err != nil {
				return "", err
			}
		}
		if !ok {
			nc := customer.Customer{ID: u.id, Phone: u.phone}
			if u.first != "" || u.last != "" {
				nc.FirstName, nc.LastName, nc.Name, nc.NameSource = u.first, u.last, customer.JoinName(u.first, u.last), customer.SourceCounter
			}
			if u.notes != "" {
				nc.Notes, nc.NotesSource = u.notes, customer.SourceCounter
			}
			if c, err = tx.CreateCounter(ctx, tenantID, locationID, nc); err != nil {
				return "", err
			}
			created = true
		} else {
			next := c
			if u.setName && (u.first != "" || u.last != "") && customer.CounterMayWrite(c.NameSource, c.Name) {
				if name := customer.JoinName(u.first, u.last); u.first != c.FirstName || u.last != c.LastName || name != c.Name {
					next.FirstName, next.LastName, next.Name, next.NameSource = u.first, u.last, name, customer.SourceCounter
				}
			}
			if u.setNotes && u.notes != c.Notes && customer.CounterMayWrite(c.NotesSource, c.Notes) {
				switch {
				case u.notes != "":
					next.Notes, next.NotesSource = u.notes, customer.SourceCounter
				case c.NotesSource == customer.SourceCounter: // the counter clears its own notes only
					next.Notes, next.NotesSource = "", customer.SourceNone
				}
			}
			if next != c {
				if err := tx.SetNamesNotes(ctx, next); err != nil {
					return "", err
				}
				c = next
			}
		}
		for _, a := range u.addresses {
			if i := addressByID(have, a.ID); i >= 0 {
				a.Default = have[i].Default
				if err := tx.UpdateAddress(ctx, c.ID, a); err != nil {
					return "", err
				}
				have[i] = a
				continue
			}
			if i := sameAddressAt(have, a); i >= 0 {
				if f, ok := fillBlanks(have[i], a); ok {
					if err := tx.FillAddress(ctx, f); err != nil {
						return "", err
					}
					have[i] = f
				}
				continue
			}
			if len(have) >= customer.MaxAddresses {
				continue
			}
			a.Default = len(have) == 0
			na, err := tx.AddAddress(ctx, c.ID, a)
			if err != nil {
				return "", err
			}
			have = append(have, na)
		}
		return resultApplied, nil
	})
	if err == nil && res == resultApplied {
		s.logger.Info().Ctx(ctx).Str("location_id", locationID.String()).Str("customer_id", u.id.String()).
			Bool("created", created).Int("addresses", len(u.addresses)).Msg("counter customer saved")
	}
	return res, err
}

// ApplyCounterEvent checks and applies a customer.upsert floor event; an
// error wrapping ErrBadCounterCustomer means main can't take it (invalid).
func (s *Service) ApplyCounterEvent(ctx context.Context, tenantID, locationID, eventID uuid.UUID, typ string, raw json.RawMessage,
	set []string) (string, error) {
	u, err := CheckCounter(raw, set)
	if err != nil {
		return "", err
	}
	return s.ApplyCounter(ctx, tenantID, locationID, eventID, typ, u)
}

func addressByID(have []customer.Address, id uuid.UUID) int {
	if id == uuid.Nil {
		return -1
	}
	for i, h := range have {
		if h.ID == id {
			return i
		}
	}
	return -1
}
