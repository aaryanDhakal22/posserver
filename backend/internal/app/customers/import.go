package customers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"quiccpos/main/internal/domain/customer"

	"github.com/google/uuid"
)

// Terminal stage 0c: the bulk import (POST /admin/v1/locations/:id/customers/import)
// and the Pi's change feed (GET /store/v1/agent/customers).

// MaxImportBatch is the most customers one import call takes.
const MaxImportBatch = 5000

// ErrTooManyRecords: a batch over MaxImportBatch.
var ErrTooManyRecords = fmt.Errorf("an import takes up to %d customers per call", MaxImportBatch)

// ImportRecord is one customer of an import.
type ImportRecord struct {
	Ref       string          `json:"ref"` // the source's id, echoed in skipped_rows only (never stored)
	Phone     string          `json:"phone"`
	FirstName string          `json:"first_name"`
	LastName  string          `json:"last_name"`
	Name      string          `json:"name"` // used (split) only when first_name and last_name are both empty
	Notes     string          `json:"notes"`
	Addresses []ImportAddress `json:"addresses"`
}

// ImportAddress is one address of an imported customer.
type ImportAddress struct {
	Label string `json:"label"`
	Line1 string `json:"line1"`
	Line2 string `json:"line2"`
	City  string `json:"city"`
	State string `json:"state"`
	Zip   string `json:"zip"`
	Notes string `json:"notes"`
}

// SkippedRow is a record the import left out (index = its 0-based position).
type SkippedRow struct {
	Index  int    `json:"index"`
	Ref    string `json:"ref,omitempty"`
	Reason string `json:"reason"`
}

// AddressCounts are an import's address outcomes.
type AddressCounts struct {
	Added       int            `json:"added"`
	Matched     int            `json:"matched"` // the customer already had it (blank fields filled)
	Skipped     int            `json:"skipped"`
	SkipReasons map[string]int `json:"skip_reasons"`
}

// ImportResult is the import's answer.
type ImportResult struct {
	DryRun      bool           `json:"dry_run"`
	Received    int            `json:"received"`
	Created     int            `json:"created"`
	Updated     int            `json:"updated"`
	Unchanged   int            `json:"unchanged"`
	Skipped     int            `json:"skipped"`
	SkipReasons map[string]int `json:"skip_reasons"`
	SkippedRows []SkippedRow   `json:"skipped_rows"` // the first 100
	// Kept counts fields the import would have written but left alone because
	// the customer (website) or staff (counter) wrote them: name, notes.
	Kept      map[string]int `json:"kept"`
	Addresses AddressCounts  `json:"addresses"`
}

const maxSkippedRows = 100

type cleanRecord struct {
	index     int
	ref       string
	phone     string
	first     string
	last      string
	notes     string
	addresses []customer.Address
}

func cleanImportAddress(a ImportAddress) (customer.Address, string) {
	t := func(s string, n int) string { return clip(strings.Join(strings.Fields(s), " "), n) }
	out := customer.Address{Label: t(a.Label, 40), Line1: t(a.Line1, 120), Line2: t(a.Line2, 120), City: t(a.City, 80),
		State: strings.ToUpper(t(a.State, 2)), Zip: t(a.Zip, 10), Notes: clip(strings.TrimSpace(a.Notes), 300)}
	switch {
	case customer.NormLine1(out.Line1) == "": // empty or only punctuation (".", "@"): it could never match itself again
		return out, "no_line1"
	case out.City == "" && out.Zip == "":
		return out, "no_city_or_zip"
	}
	return out, ""
}

// Import upserts a batch of customers into one store by phone: a new phone
// is a new customer (source import); an existing one gets the record's name
// and notes only where the import may write them (empty, or written by an
// earlier import — never the website's or the counter's), and its addresses
// are added unless the customer already has the same place (normalised
// line 1 + 5-digit zip), whose blank fields are filled instead. A dry run
// does all of it in a transaction that is rolled back.
func (s *Service) Import(ctx context.Context, tenantID, locationID uuid.UUID, recs []ImportRecord, dryRun bool) (ImportResult, error) {
	if len(recs) > MaxImportBatch {
		return ImportResult{}, ErrTooManyRecords
	}
	res := ImportResult{DryRun: dryRun, Received: len(recs), SkipReasons: map[string]int{}, SkippedRows: []SkippedRow{},
		Kept: map[string]int{}, Addresses: AddressCounts{SkipReasons: map[string]int{}}}
	skip := func(i int, ref, reason string) {
		res.Skipped++
		res.SkipReasons[reason]++
		if len(res.SkippedRows) < maxSkippedRows {
			res.SkippedRows = append(res.SkippedRows, SkippedRow{Index: i, Ref: clip(ref, 64), Reason: reason})
		}
	}
	addrSkip := func(reason string) {
		res.Addresses.Skipped++
		res.Addresses.SkipReasons[reason]++
	}

	clean := make([]cleanRecord, 0, len(recs))
	phones := make([]string, 0, len(recs))
	for i, r := range recs {
		p := NormPhone(r.Phone)
		if p == "" {
			skip(i, r.Ref, "bad_phone")
			continue
		}
		first, last := clip(strings.Join(strings.Fields(r.FirstName), " "), 60), clip(strings.Join(strings.Fields(r.LastName), " "), 60)
		if first == "" && last == "" {
			first, last = customer.SplitName(clip(r.Name, 120))
		}
		cr := cleanRecord{index: i, ref: r.Ref, phone: p, first: first, last: last, notes: clip(strings.TrimSpace(r.Notes), 1000)}
		for _, a := range r.Addresses {
			ca, why := cleanImportAddress(a)
			if why != "" {
				addrSkip(why)
				continue
			}
			cr.addresses = append(cr.addresses, ca)
		}
		if cr.first == "" && cr.last == "" && cr.notes == "" && len(cr.addresses) == 0 {
			skip(i, r.Ref, "empty")
			continue
		}
		clean = append(clean, cr)
		phones = append(phones, p)
	}

	err := s.repo.Import(ctx, !dryRun, func(tx customer.ImportTx) error {
		known, addrs, err := tx.ByPhones(ctx, locationID, phones)
		if err != nil {
			return err
		}
		for _, r := range clean {
			c, exists := known[r.phone]
			changed := false
			if !exists {
				c, err = tx.Create(ctx, tenantID, locationID, customer.Customer{Phone: r.phone, FirstName: r.first, LastName: r.last,
					Name: customer.JoinName(r.first, r.last), Notes: r.notes})
				if err != nil {
					return fmt.Errorf("record %d: %w", r.index, err)
				}
			} else {
				next := c
				if r.first != "" || r.last != "" {
					name := customer.JoinName(r.first, r.last)
					switch {
					case !customer.ImportMayWrite(c.NameSource, c.Name):
						if name != c.Name {
							res.Kept["name"]++
						}
					case r.first != c.FirstName || r.last != c.LastName || name != c.Name:
						next.FirstName, next.LastName, next.Name, next.NameSource = r.first, r.last, name, customer.SourceImport
					}
				}
				if r.notes != "" {
					switch {
					case !customer.ImportMayWrite(c.NotesSource, c.Notes):
						if r.notes != c.Notes {
							res.Kept["notes"]++
						}
					case r.notes != c.Notes:
						next.Notes, next.NotesSource = r.notes, customer.SourceImport
					}
				}
				if next != c {
					if err := tx.SetNamesNotes(ctx, next); err != nil {
						return fmt.Errorf("record %d: %w", r.index, err)
					}
					c, changed = next, true
				}
			}
			have := addrs[c.ID]
			for _, a := range r.addresses {
				if i := sameAddressAt(have, a); i >= 0 {
					res.Addresses.Matched++
					if f, ok := fillBlanks(have[i], a); ok {
						if err := tx.FillAddress(ctx, f); err != nil {
							return fmt.Errorf("record %d: %w", r.index, err)
						}
						have[i], changed = f, true
					}
					continue
				}
				if len(have) >= customer.MaxAddresses {
					addrSkip("limit")
					continue
				}
				a.Default = len(have) == 0
				na, err := tx.AddAddress(ctx, c.ID, a)
				if err != nil {
					return fmt.Errorf("record %d: %w", r.index, err)
				}
				have = append(have, na)
				res.Addresses.Added++
				changed = true
			}
			addrs[c.ID] = have
			switch {
			case !exists:
				res.Created++
				known[r.phone] = c // a later record with the same phone updates this one
			case changed:
				res.Updated++
				known[r.phone] = c
			default:
				res.Unchanged++
			}
		}
		return nil
	})
	if err != nil {
		return ImportResult{}, err
	}
	s.logger.Info().Str("location_id", locationID.String()).Bool("dry_run", dryRun).Int("received", res.Received).
		Int("created", res.Created).Int("updated", res.Updated).Int("unchanged", res.Unchanged).Int("skipped", res.Skipped).
		Int("addresses_added", res.Addresses.Added).Msg("customers imported")
	return res, nil
}

func sameAddressAt(have []customer.Address, a customer.Address) int {
	for i, h := range have {
		if customer.SameAddress(h, a) {
			return i
		}
	}
	return -1
}

// fillBlanks fills the existing address's empty fields from the imported one
// (never overwrites); ok reports a change.
func fillBlanks(have, in customer.Address) (customer.Address, bool) {
	out := have
	for _, f := range []struct {
		dst *string
		src string
	}{
		{&out.Label, in.Label}, {&out.Line2, in.Line2}, {&out.City, in.City}, {&out.State, in.State}, {&out.Zip, in.Zip}, {&out.Notes, in.Notes},
	} {
		if *f.dst == "" && f.src != "" {
			*f.dst = f.src
		}
	}
	return out, out != have
}

// --- the Pi's change feed --------------------------------------------------------------

// FeedLag: the feed's next cursor never passes now − FeedLag, so a change
// whose transaction commits a little after its timestamp is still sent
// (rows inside the window may come twice; the Pi upserts by id).
const FeedLag = 2 * time.Minute

// TombstoneKeep is how long deletions are remembered; a Pi further behind
// gets reset (start its copy over from an empty cursor).
const TombstoneKeep = 90 * 24 * time.Hour

// FeedCustomer is one customer as the Pi keeps it.
type FeedCustomer struct {
	ID        uuid.UUID          `json:"id"`
	Phone     string             `json:"phone"`
	FirstName string             `json:"first_name"`
	LastName  string             `json:"last_name"`
	Name      string             `json:"name"`
	Notes     string             `json:"notes"`
	Source    string             `json:"source"`
	UpdatedAt time.Time          `json:"updated_at"`
	Addresses []customer.Address `json:"addresses"`
}

// Feed is GET /store/v1/agent/customers.
type Feed struct {
	Customers []FeedCustomer `json:"customers"`
	Deleted   []uuid.UUID    `json:"deleted"`
	Cursor    string         `json:"cursor"`
	HasMore   bool           `json:"has_more"`
	Reset     bool           `json:"reset"`
}

// ErrBadCursor: a since= the feed didn't make.
var ErrBadCursor = errors.New("bad cursor: send back the cursor the feed gave, or none")

func feedCursor(at time.Time, id uuid.UUID) string {
	return fmt.Sprintf("%d_%s", at.UnixMicro(), id)
}

func parseCursor(s string) (time.Time, uuid.UUID, error) {
	if s == "" {
		return time.Unix(0, 0).UTC(), uuid.Nil, nil
	}
	us, id, ok := strings.Cut(s, "_")
	if !ok {
		return time.Time{}, uuid.Nil, ErrBadCursor
	}
	var n int64
	if _, err := fmt.Sscanf(us, "%d", &n); err != nil {
		return time.Time{}, uuid.Nil, ErrBadCursor
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrBadCursor
	}
	return time.UnixMicro(n).UTC(), u, nil
}

// Changes is one page of the store's customer changes after since ("" =
// everything): changed customers in full (with addresses), deleted ids, the
// cursor for the next call and has_more while a full page came back.
func (s *Service) Changes(ctx context.Context, locationID uuid.UUID, since string, limit int) (Feed, error) {
	afterAt, afterID, err := parseCursor(since)
	if err != nil {
		return Feed{}, err
	}
	now := s.now()
	out := Feed{Customers: []FeedCustomer{}, Deleted: []uuid.UUID{}}
	if since != "" && afterAt.Before(now.Add(-TombstoneKeep)) {
		out.Reset = true
		return out, nil
	}
	rows, err := s.repo.Changes(ctx, locationID, afterAt, afterID, limit+1)
	if err != nil {
		return Feed{}, err
	}
	out.HasMore = len(rows) > limit
	if out.HasMore {
		rows = rows[:limit]
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		if r.Deleted {
			out.Deleted = append(out.Deleted, r.ID)
		} else {
			ids = append(ids, r.ID)
		}
	}
	cs, addrs, err := s.repo.ByIDs(ctx, ids)
	if err != nil {
		return Feed{}, err
	}
	byID := make(map[uuid.UUID]customer.Customer, len(cs))
	for _, c := range cs {
		byID[c.ID] = c
	}
	for _, id := range ids { // the feed's order (oldest change first)
		c, ok := byID[id]
		if !ok {
			continue // deleted since; its tombstone comes on a later page
		}
		as := addrs[id]
		if as == nil {
			as = []customer.Address{}
		}
		out.Customers = append(out.Customers, FeedCustomer{ID: c.ID, Phone: c.Phone, FirstName: c.FirstName, LastName: c.LastName,
			Name: c.Name, Notes: c.Notes, Source: c.Source, UpdatedAt: c.UpdatedAt, Addresses: as})
	}
	lastAt, lastID := afterAt, afterID
	if len(rows) > 0 {
		lastAt, lastID = rows[len(rows)-1].At, rows[len(rows)-1].ID
	}
	// The last page's cursor is now − FeedLag: everything older has been
	// sent (so a quiet store's cursor still moves on and never ages into a
	// reset), and the rows in the window come again on the next poll (it may
	// step back past an earlier page's cursor for that).
	if !out.HasMore {
		lastAt, lastID = now.Add(-FeedLag), uuid.Nil
	}
	out.Cursor = feedCursor(lastAt, lastID)
	return out, nil
}
