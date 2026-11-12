package customers

import (
	"context"
	"testing"
	"time"

	"quiccpos/main/internal/domain/customer"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// fakeRepo keeps one store's customers in memory (synthetic data only).
type fakeRepo struct {
	Repo    // the rest is unused here
	byPhone map[string]customer.Customer
	addrs   map[uuid.UUID][]customer.Address
	changes []customer.Change
}

func newFake() *fakeRepo {
	return &fakeRepo{byPhone: map[string]customer.Customer{}, addrs: map[uuid.UUID][]customer.Address{}}
}

func (f *fakeRepo) Import(ctx context.Context, commit bool, fn func(customer.ImportTx) error) error {
	// Work on copies; keep them only on commit.
	tx := &fakeTx{byPhone: map[string]customer.Customer{}, addrs: map[uuid.UUID][]customer.Address{}}
	for k, v := range f.byPhone {
		tx.byPhone[k] = v
	}
	for k, v := range f.addrs {
		tx.addrs[k] = append([]customer.Address(nil), v...)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if commit {
		f.byPhone, f.addrs = tx.byPhone, tx.addrs
	}
	return nil
}

type fakeTx struct {
	byPhone map[string]customer.Customer
	addrs   map[uuid.UUID][]customer.Address
}

func (t *fakeTx) ByPhones(_ context.Context, _ uuid.UUID, phones []string) (map[string]customer.Customer, map[uuid.UUID][]customer.Address, error) {
	out, as := map[string]customer.Customer{}, map[uuid.UUID][]customer.Address{}
	for _, p := range phones {
		if c, ok := t.byPhone[p]; ok {
			out[p] = c
			as[c.ID] = append([]customer.Address(nil), t.addrs[c.ID]...)
		}
	}
	return out, as, nil
}

func (t *fakeTx) Create(_ context.Context, _, _ uuid.UUID, c customer.Customer) (customer.Customer, error) {
	c.ID, c.Source = uuid.New(), customer.SourceImport
	if c.Name != "" {
		c.NameSource = customer.SourceImport
	}
	if c.Notes != "" {
		c.NotesSource = customer.SourceImport
	}
	t.byPhone[c.Phone] = c
	return c, nil
}

func (t *fakeTx) SetNamesNotes(_ context.Context, c customer.Customer) error {
	t.byPhone[c.Phone] = c
	return nil
}

func (t *fakeTx) AddAddress(_ context.Context, id uuid.UUID, a customer.Address) (customer.Address, error) {
	if a.ID == uuid.Nil { // the counter keeps the Pi's id
		a.ID = uuid.New()
	}
	t.addrs[id] = append(t.addrs[id], a)
	return a, nil
}

func (t *fakeTx) FillAddress(_ context.Context, a customer.Address) error {
	for id, as := range t.addrs {
		for i := range as {
			if as[i].ID == a.ID {
				t.addrs[id][i] = a
			}
		}
	}
	return nil
}

func svcWith(f *fakeRepo) *Service { return NewService(f, nil, zerolog.Nop()) }

var tenant, loc = uuid.New(), uuid.New()

func TestImportCreatesAndSkips(t *testing.T) {
	f := newFake()
	s := svcWith(f)
	res, err := s.Import(context.Background(), tenant, loc, []ImportRecord{
		{Ref: "1", Phone: "(410) 555-0101", FirstName: "ADA", LastName: "Test", Notes: "ring twice",
			Addresses: []ImportAddress{{Line1: "1 Test Street", City: "Towson", State: "md", Zip: "21286"},
				{Line1: "1 test st.", Zip: "21286"}, // the same place: matched
				{Line1: "", City: "Towson"},         // no line1
				{Line1: "2 Test Rd"}}},              // no city or zip
		{Ref: "2", Phone: "555-0101"},                          // 7 digits
		{Ref: "3", Phone: "4105550102"},                        // nothing to keep
		{Ref: "4", Phone: "1-410-555-0103", Name: "Bo Q Test"}, // name split
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Received != 4 || res.Created != 2 || res.Skipped != 2 || res.SkipReasons["bad_phone"] != 1 || res.SkipReasons["empty"] != 1 {
		t.Fatalf("counts: %+v", res)
	}
	if res.Addresses.Added != 1 || res.Addresses.Matched != 1 || res.Addresses.SkipReasons["no_line1"] != 1 ||
		res.Addresses.SkipReasons["no_city_or_zip"] != 1 {
		t.Fatalf("addresses: %+v", res.Addresses)
	}
	if len(res.SkippedRows) != 2 || res.SkippedRows[0].Ref != "2" || res.SkippedRows[0].Index != 1 {
		t.Fatalf("skipped rows: %+v", res.SkippedRows)
	}
	c := f.byPhone["4105550101"]
	if c.Name != "ADA Test" || c.Notes != "ring twice" || c.Source != customer.SourceImport {
		t.Fatalf("customer: %+v", c)
	}
	if a := f.addrs[c.ID]; len(a) != 1 || !a[0].Default || a[0].State != "MD" {
		t.Fatalf("address: %+v", a)
	}
	if b := f.byPhone["4105550103"]; b.FirstName != "Bo Q" || b.LastName != "Test" || b.Name != "Bo Q Test" {
		t.Fatalf("split: %+v", b)
	}
}

func TestImportNeverOverwritesTheWebsitesName(t *testing.T) {
	f := newFake()
	id := uuid.New()
	f.byPhone["4105550101"] = customer.Customer{ID: id, Phone: "4105550101", Name: "Ada Own", FirstName: "Ada", LastName: "Own",
		NameSource: customer.SourceWeb, Notes: "staff note", NotesSource: customer.SourceCounter, Source: customer.SourceWeb}
	f.addrs[id] = []customer.Address{{ID: uuid.New(), Line1: "1 Test St", Zip: "21286", Default: true}}
	s := svcWith(f)
	res, err := s.Import(context.Background(), tenant, loc, []ImportRecord{{Phone: "4105550101", FirstName: "Old", LastName: "Name",
		Notes: "old note", Addresses: []ImportAddress{{Line1: "1 Test Street", City: "Towson", Zip: "21286", Notes: "side door"}}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	c := f.byPhone["4105550101"]
	if c.Name != "Ada Own" || c.Notes != "staff note" || res.Kept["name"] != 1 || res.Kept["notes"] != 1 {
		t.Fatalf("overwrote: %+v %+v", c, res)
	}
	// The matched address got its blanks filled (city, notes), the zip/line1 kept.
	if a := f.addrs[id]; len(a) != 1 || a[0].City != "Towson" || a[0].Notes != "side door" || a[0].Line1 != "1 Test St" {
		t.Fatalf("address: %+v", a)
	}
	if res.Updated != 1 || res.Created != 0 {
		t.Fatalf("counts: %+v", res)
	}
	// Again: nothing left to change.
	res, _ = s.Import(context.Background(), tenant, loc, []ImportRecord{{Phone: "4105550101", FirstName: "Old", LastName: "Name",
		Addresses: []ImportAddress{{Line1: "1 Test St", Zip: "21286"}}}}, false)
	if res.Unchanged != 1 {
		t.Fatalf("second run: %+v", res)
	}
}

func TestImportUpdatesAnImportedNameAndDryRunKeepsNothing(t *testing.T) {
	f := newFake()
	s := svcWith(f)
	rec := ImportRecord{Phone: "4105550101", FirstName: "Ada", LastName: "Test"}
	if res, _ := s.Import(context.Background(), tenant, loc, []ImportRecord{rec}, true); res.Created != 1 || !res.DryRun {
		t.Fatalf("dry run: %+v", res)
	}
	if len(f.byPhone) != 0 {
		t.Fatal("a dry run saved")
	}
	_, _ = s.Import(context.Background(), tenant, loc, []ImportRecord{rec}, false)
	rec.LastName = "Tester"
	res, _ := s.Import(context.Background(), tenant, loc, []ImportRecord{rec, {Phone: "410-555-0101", Notes: "later line"}}, false)
	if res.Updated != 2 || f.byPhone["4105550101"].Name != "Ada Tester" || f.byPhone["4105550101"].Notes != "later line" {
		t.Fatalf("re-import: %+v %+v", res, f.byPhone["4105550101"])
	}
}

func TestImportAddressLimit(t *testing.T) {
	f := newFake()
	s := svcWith(f)
	var as []ImportAddress
	for i := range 22 {
		as = append(as, ImportAddress{Line1: string(rune('A'+i)) + " Test St", Zip: "21286"})
	}
	res, _ := s.Import(context.Background(), tenant, loc, []ImportRecord{{Phone: "4105550101", FirstName: "Ada", Addresses: as}}, false)
	if res.Addresses.Added != 20 || res.Addresses.SkipReasons["limit"] != 2 {
		t.Fatalf("%+v", res.Addresses)
	}
}

// A line 1 of punctuation only ("." is a common placeholder) normalises to nothing, so it could never
// match itself: every re-run would add it again. It is skipped as no_line1, and a re-run is unchanged.
func TestImportPunctuationLine1IsSkippedAndReRunIsUnchanged(t *testing.T) {
	f := newFake()
	s := svcWith(f)
	recs := []ImportRecord{{Phone: "4105550101", FirstName: "Ada", Addresses: []ImportAddress{
		{Line1: ".", City: "Towson", Zip: "21286"}, {Line1: "@", Zip: "21286"}, {Line1: "1 Test St", Zip: "21286"}}}}
	res, _ := s.Import(context.Background(), tenant, loc, recs, false)
	if res.Addresses.Added != 1 || res.Addresses.SkipReasons["no_line1"] != 2 {
		t.Fatalf("first: %+v", res.Addresses)
	}
	res, _ = s.Import(context.Background(), tenant, loc, recs, false)
	if res.Unchanged != 1 || res.Addresses.Added != 0 || res.Addresses.Matched != 1 {
		t.Fatalf("again: %+v / %+v", res, res.Addresses)
	}
}

func TestImportTooMany(t *testing.T) {
	s := svcWith(newFake())
	if _, err := s.Import(context.Background(), tenant, loc, make([]ImportRecord, MaxImportBatch+1), false); err != ErrTooManyRecords {
		t.Fatalf("err = %v", err)
	}
}

// --- the change feed ---

func (f *fakeRepo) Changes(_ context.Context, _ uuid.UUID, afterAt time.Time, afterID uuid.UUID, limit int) ([]customer.Change, error) {
	var out []customer.Change
	for _, c := range f.changes {
		if c.At.After(afterAt) || (c.At.Equal(afterAt) && c.ID.String() > afterID.String()) {
			out = append(out, c)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeRepo) ByIDs(_ context.Context, ids []uuid.UUID) ([]customer.Customer, map[uuid.UUID][]customer.Address, error) {
	var out []customer.Customer
	for _, id := range ids {
		for _, c := range f.byPhone {
			if c.ID == id {
				out = append(out, c)
			}
		}
	}
	return out, f.addrs, nil
}

func TestFeedPagesAndLags(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	f := newFake()
	var ids []uuid.UUID
	for i := range 3 {
		id := uuid.New()
		ids = append(ids, id)
		f.byPhone[string(rune('a'+i))] = customer.Customer{ID: id, Phone: "41055501" + string(rune('0'+i)) + "0", Name: "T"}
		f.changes = append(f.changes, customer.Change{ID: id, At: now.Add(time.Duration(i-10) * time.Minute)})
	}
	gone := uuid.New()
	f.changes = append(f.changes, customer.Change{ID: gone, At: now.Add(-30 * time.Second), Deleted: true})
	s := svcWith(f)
	s.now = func() time.Time { return now }

	p1, err := s.Changes(context.Background(), loc, "", 2)
	if err != nil || !p1.HasMore || len(p1.Customers) != 2 || p1.Customers[0].ID != ids[0] {
		t.Fatalf("page 1: %+v %v", p1, err)
	}
	p2, err := s.Changes(context.Background(), loc, p1.Cursor, 2)
	if err != nil || p2.HasMore || len(p2.Customers) != 1 || len(p2.Deleted) != 1 || p2.Deleted[0] != gone {
		t.Fatalf("page 2: %+v %v", p2, err)
	}
	// The last cursor sits FeedLag behind now: the deletion inside the window comes again.
	p3, _ := s.Changes(context.Background(), loc, p2.Cursor, 2)
	if len(p3.Customers) != 0 || len(p3.Deleted) != 1 {
		t.Fatalf("page 3: %+v", p3)
	}
	if _, err := s.Changes(context.Background(), loc, "nonsense", 2); err != ErrBadCursor {
		t.Fatalf("bad cursor: %v", err)
	}
	old := feedCursor(now.Add(-TombstoneKeep-time.Hour), uuid.Nil)
	if r, _ := s.Changes(context.Background(), loc, old, 2); !r.Reset {
		t.Fatal("an old cursor must reset")
	}
}
