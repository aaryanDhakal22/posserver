package customers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"quiccpos/main/internal/domain/customer"

	"github.com/google/uuid"
)

// The counter's customer.upsert (terminal 1d) on the in-memory store of
// import_test.go. Synthetic data only.

func (f *fakeRepo) CounterUpsert(ctx context.Context, _, _, _ uuid.UUID, _ string, fn func(customer.CounterTx) (string, error)) (string, error) {
	var res string
	err := f.Import(ctx, uuid.Nil, true, func(tx customer.ImportTx) error {
		var err error
		res, err = fn(tx.(*fakeTx))
		return err
	})
	return res, err
}

func (t *fakeTx) ByID(_ context.Context, _, id uuid.UUID) (customer.Customer, []customer.Address, bool, error) {
	for _, c := range t.byPhone {
		if c.ID == id {
			return c, append([]customer.Address(nil), t.addrs[c.ID]...), true, nil
		}
	}
	return customer.Customer{}, nil, false, nil
}

func (t *fakeTx) ByPhone(_ context.Context, _ uuid.UUID, phone string) (customer.Customer, []customer.Address, bool, error) {
	c, ok := t.byPhone[phone]
	return c, append([]customer.Address(nil), t.addrs[c.ID]...), ok, nil
}

func (t *fakeTx) CreateCounter(_ context.Context, _, _ uuid.UUID, c customer.Customer) (customer.Customer, error) {
	c.Source = customer.SourceCounter
	t.byPhone[c.Phone] = c
	return c, nil
}

func (t *fakeTx) UpdateAddress(_ context.Context, cid uuid.UUID, a customer.Address) error {
	for i := range t.addrs[cid] {
		if t.addrs[cid][i].ID == a.ID {
			t.addrs[cid][i] = a
		}
	}
	return nil
}

func upsert(t *testing.T, s *Service, c CounterCustomer, set ...string) string {
	t.Helper()
	raw, _ := json.Marshal(c)
	res, err := s.ApplyCounterEvent(context.Background(), tenant, loc, uuid.New(), "customer.upsert", raw, set)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	return res
}

func TestCounterUpsertCreatesUnderThePisIDs(t *testing.T) {
	f := newFake()
	s := svcWith(f)
	id, aid := uuid.New(), uuid.New()
	res := upsert(t, s, CounterCustomer{ID: id.String(), Phone: "410-555-0101", FirstName: " Ada ", LastName: "Test", Notes: "side door",
		Addresses: []CounterAddress{{ID: aid.String(), Line1: "1 Test Street", Line2: "Apt 2", City: "Towson", Zip: "21286"}}}, SetName, SetNotes)
	c := f.byPhone["4105550101"]
	if res != "applied" || c.ID != id || c.Source != customer.SourceCounter || c.Name != "Ada Test" || c.NameSource != customer.SourceCounter ||
		c.Notes != "side door" || c.NotesSource != customer.SourceCounter {
		t.Fatalf("%s %+v", res, c)
	}
	if as := f.addrs[id]; len(as) != 1 || as[0].ID != aid || !as[0].Default || as[0].Line2 != "Apt 2" {
		t.Fatalf("addresses %+v", as)
	}
}

func TestCounterUpsertKeepsTheWebsitesNameAndMergesByPhone(t *testing.T) {
	f := newFake()
	s := svcWith(f)
	web := customer.Customer{ID: uuid.New(), Phone: "4105550102", Name: "Bo Web", FirstName: "Bo", LastName: "Web", NameSource: customer.SourceWeb,
		Notes: "imported note", NotesSource: customer.SourceImport}
	f.byPhone[web.Phone] = web
	home := customer.Address{ID: uuid.New(), Line1: "2 Test Avenue", Zip: "21286", Default: true}
	f.addrs[web.ID] = []customer.Address{home}

	// The Pi made its own copy offline (another id): merged by phone.
	piID := uuid.New()
	res := upsert(t, s, CounterCustomer{ID: piID.String(), Phone: "4105550102", FirstName: "Robert", Notes: "counter note",
		Addresses: []CounterAddress{{ID: uuid.NewString(), Line1: "2 test ave.", City: "Towson", Zip: "21286-1234"},
			{ID: uuid.NewString(), Line1: "9 Other Rd", City: "Towson"}}}, SetName, SetNotes)
	c := f.byPhone["4105550102"]
	if res != "applied" || c.ID != web.ID || c.Name != "Bo Web" || c.NameSource != customer.SourceWeb {
		t.Fatalf("the website's name must stay: %s %+v", res, c)
	}
	if c.Notes != "counter note" || c.NotesSource != customer.SourceCounter {
		t.Fatalf("notes over an import's: %+v", c)
	}
	as := f.addrs[web.ID]
	if len(as) != 2 || as[0].ID != home.ID || as[0].City != "Towson" || as[0].Line1 != "2 Test Avenue" || as[1].Line1 != "9 Other Rd" || as[1].Default {
		t.Fatalf("same place fills blanks, a new one is added: %+v", as)
	}

	// Clearing notes: only the counter's own.
	upsert(t, s, CounterCustomer{ID: web.ID.String(), Phone: "4105550102"}, SetNotes)
	if c := f.byPhone["4105550102"]; c.Notes != "" || c.NotesSource != customer.SourceNone {
		t.Fatalf("the counter clears its own notes: %+v", c)
	}
	// A field not in `set` is left alone; an empty name never blanks one.
	upsert(t, s, CounterCustomer{ID: web.ID.String(), Phone: "4105550102", Notes: "not set"}, SetName)
	if c := f.byPhone["4105550102"]; c.Notes != "" || c.Name != "Bo Web" {
		t.Fatalf("%+v", c)
	}
}

func TestCounterUpsertEditsAnAddressByID(t *testing.T) {
	f := newFake()
	s := svcWith(f)
	id, aid := uuid.New(), uuid.New()
	upsert(t, s, CounterCustomer{ID: id.String(), Phone: "4105550103", FirstName: "Cy",
		Addresses: []CounterAddress{{ID: aid.String(), Line1: "3 Test St", Line2: "Apt 1", City: "Towson"}}}, SetName)
	upsert(t, s, CounterCustomer{ID: id.String(), Phone: "4105550103", FirstName: "Cyrus", LastName: "Test",
		Addresses: []CounterAddress{{ID: aid.String(), Line1: "3 Test St", Line2: "", City: "Towson", Notes: "gate 4"}}}, SetName)
	c := f.byPhone["4105550103"]
	as := f.addrs[id]
	if c.Name != "Cyrus Test" || len(as) != 1 || as[0].Line2 != "" || as[0].Notes != "gate 4" || !as[0].Default {
		t.Fatalf("%+v %+v", c, as)
	}
	// A changed phone under a known id is refused.
	if res := upsert(t, s, CounterCustomer{ID: id.String(), Phone: "4105550199", FirstName: "Cy"}, SetName); res != "invalid" {
		t.Fatalf("phone change: %s", res)
	}
}

func TestCheckCounterRefusesBadEvents(t *testing.T) {
	for name, c := range map[string]string{
		"no id":        `{"phone":"4105550101"}`,
		"bad phone":    `{"id":"` + uuid.NewString() + `","phone":"555-0101"}`,
		"bad address":  `{"id":"` + uuid.NewString() + `","phone":"4105550101","addresses":[{"line1":"","city":"x"}]}`,
		"address id":   `{"id":"` + uuid.NewString() + `","phone":"4105550101","addresses":[{"id":"x","line1":"1 A St","city":"x"}]}`,
		"not a object": `[1]`,
	} {
		if _, err := CheckCounter(json.RawMessage(c), nil); !errors.Is(err, ErrBadCounterCustomer) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := CheckCounter(json.RawMessage(`{"id":"`+uuid.NewString()+`","phone":"4105550101"}`), []string{"email"}); !errors.Is(err, ErrBadCounterCustomer) {
		t.Errorf("unknown set field: %v", err)
	}
}
