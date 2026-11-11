package repositories

import (
	"context"
	"time"

	"quiccpos/main/internal/domain/customer"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// Terminal stage 0c: the bulk customer import and the Pi's change feed.

// Import runs fn in one transaction; commit false (a dry run) rolls it back.
func (r *CustomerRepository) Import(ctx context.Context, commit bool, fn func(customer.ImportTx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(importTx{q: models.New(tx)}); err != nil {
		return err
	}
	if !commit {
		return nil
	}
	return tx.Commit(ctx)
}

type importTx struct{ q *models.Queries }

func (t importTx) ByPhones(ctx context.Context, locationID uuid.UUID, phones []string) (map[string]customer.Customer,
	map[uuid.UUID][]customer.Address, error) {
	rows, err := t.q.ListCustomersByPhones(ctx, models.ListCustomersByPhonesParams{LocationID: pgID(locationID), Phones: phones})
	if err != nil {
		return nil, nil, err
	}
	out := make(map[string]customer.Customer, len(rows))
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, x := range rows {
		out[x.Phone] = customerOf(x)
		ids = append(ids, x.ID)
	}
	addrs, err := addressesOf(ctx, t.q, ids)
	return out, addrs, err
}

func addressesOf(ctx context.Context, q *models.Queries, ids []pgtype.UUID) (map[uuid.UUID][]customer.Address, error) {
	out := map[uuid.UUID][]customer.Address{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.ListAddressesOfCustomers(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, a := range rows {
		id := idOf(a.CustomerID)
		out[id] = append(out[id], addressOf(a))
	}
	return out, nil
}

func (t importTx) Create(ctx context.Context, tenantID, locationID uuid.UUID, c customer.Customer) (customer.Customer, error) {
	row, err := t.q.InsertImportedCustomer(ctx, models.InsertImportedCustomerParams{TenantID: pgID(tenantID), LocationID: pgID(locationID),
		Phone: c.Phone, Name: c.Name, FirstName: c.FirstName, LastName: c.LastName, Notes: c.Notes})
	if err != nil {
		return customer.Customer{}, err
	}
	return customerOf(row), nil
}

func (t importTx) SetNamesNotes(ctx context.Context, c customer.Customer) error {
	return t.q.UpdateImportedCustomer(ctx, models.UpdateImportedCustomerParams{ID: pgID(c.ID), Name: c.Name, FirstName: c.FirstName,
		LastName: c.LastName, NameSource: c.NameSource, Notes: c.Notes, NotesSource: c.NotesSource})
}

func (t importTx) AddAddress(ctx context.Context, customerID uuid.UUID, a customer.Address) (customer.Address, error) {
	row, err := t.q.InsertCustomerAddress(ctx, models.InsertCustomerAddressParams{CustomerID: pgID(customerID), Label: a.Label,
		Line1: a.Line1, Line2: a.Line2, City: a.City, State: a.State, Zip: a.Zip, Notes: a.Notes, IsDefault: a.Default})
	if err != nil {
		return customer.Address{}, err
	}
	return addressOf(row), nil
}

func (t importTx) FillAddress(ctx context.Context, a customer.Address) error {
	return t.q.FillCustomerAddress(ctx, models.FillCustomerAddressParams{ID: pgID(a.ID), Label: a.Label, Line2: a.Line2, City: a.City,
		State: a.State, Zip: a.Zip, Notes: a.Notes})
}

// Changes are the store's customer changes + deletions after (afterAt,
// afterID), oldest first, at most limit.
func (r *CustomerRepository) Changes(ctx context.Context, locationID uuid.UUID, afterAt time.Time, afterID uuid.UUID,
	limit int) ([]customer.Change, error) {
	rows, err := models.New(r.pool).CustomerChanges(ctx, models.CustomerChangesParams{LocationID: pgID(locationID),
		AfterAt: ts(afterAt), AfterID: pgID(afterID), MaxRows: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]customer.Change, 0, len(rows))
	for _, x := range rows {
		out = append(out, customer.Change{ID: idOf(x.ID), At: x.At.Time, Deleted: x.Deleted})
	}
	return out, nil
}

// ByIDs are these customers with their addresses (a gone id is left out).
func (r *CustomerRepository) ByIDs(ctx context.Context, ids []uuid.UUID) ([]customer.Customer, map[uuid.UUID][]customer.Address, error) {
	q := models.New(r.pool)
	pg := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		pg = append(pg, pgID(id))
	}
	if len(pg) == 0 {
		return nil, map[uuid.UUID][]customer.Address{}, nil
	}
	rows, err := q.ListCustomersByIDs(ctx, pg)
	if err != nil {
		return nil, nil, err
	}
	out := make([]customer.Customer, 0, len(rows))
	for _, x := range rows {
		out = append(out, customerOf(x))
	}
	addrs, err := addressesOf(ctx, q, pg)
	return out, addrs, err
}

// PruneTombstones forgets deletions older than before.
func (r *CustomerRepository) PruneTombstones(ctx context.Context, before time.Time) (int64, error) {
	return models.New(r.pool).PruneCustomerTombstones(ctx, ts(before))
}
