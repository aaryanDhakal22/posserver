package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"quiccpos/main/internal/domain/customer"
	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Terminal 1d: customers made or changed at the counter (the Pi's floor
// event customer.upsert). Plain SQL for the writes that take the Pi's ids.

// CounterUpsert runs fn in one transaction and marks the floor event seen
// with its result; an event seen already answers duplicate (fn's work is
// rolled back).
func (r *CustomerRepository) CounterUpsert(ctx context.Context, tenantID, locationID, eventID uuid.UUID, typ string,
	fn func(tx customer.CounterTx) (string, error)) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := models.New(tx)
	res, err := fn(counterTx{tx: tx, q: q})
	if err != nil {
		return "", err
	}
	fresh, err := markSeen(ctx, q, order.Scope{TenantID: order.TenantID(tenantID), LocationID: order.LocationID(locationID)},
		eventID, nil, typ, res)
	if err != nil {
		return "", err
	}
	if !fresh {
		return order.FloorDuplicate, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return res, nil
}

type counterTx struct {
	tx pgx.Tx
	q  *models.Queries
}

func (t counterTx) one(ctx context.Context, rows []models.Customer, err error) (customer.Customer, []customer.Address, bool, error) {
	if err != nil || len(rows) == 0 {
		return customer.Customer{}, nil, false, err
	}
	c := customerOf(rows[0])
	addrs, err := addressesOf(ctx, t.q, []pgtype.UUID{rows[0].ID})
	if err != nil {
		return customer.Customer{}, nil, false, err
	}
	return c, addrs[c.ID], true, nil
}

func (t counterTx) ByID(ctx context.Context, locationID, id uuid.UUID) (customer.Customer, []customer.Address, bool, error) {
	rows, err := t.q.ListCustomersByIDs(ctx, []pgtype.UUID{pgID(id)})
	if err == nil && len(rows) > 0 && idOf(rows[0].LocationID) != locationID {
		rows = nil // another store's customer: never touched from here
	}
	return t.one(ctx, rows, err)
}

func (t counterTx) ByPhone(ctx context.Context, locationID uuid.UUID, phone string) (customer.Customer, []customer.Address, bool, error) {
	rows, err := t.q.ListCustomersByPhones(ctx, models.ListCustomersByPhonesParams{LocationID: pgID(locationID), Phones: []string{phone}})
	return t.one(ctx, rows, err)
}

func (t counterTx) CreateCounter(ctx context.Context, tenantID, locationID uuid.UUID, c customer.Customer) (customer.Customer, error) {
	// The Pi's id, unless some customer has it already (a new one then).
	tag, err := t.tx.Exec(ctx, `INSERT INTO customers (id, tenant_id, location_id, phone, name, first_name, last_name, notes,
		source, name_source, notes_source) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'counter', $9, $10) ON CONFLICT (id) DO NOTHING`,
		c.ID, tenantID, locationID, c.Phone, c.Name, c.FirstName, c.LastName, c.Notes, c.NameSource, c.NotesSource)
	if err != nil {
		return customer.Customer{}, fmt.Errorf("insert counter customer: %w", err)
	}
	if tag.RowsAffected() == 0 {
		c.ID = uuid.New()
		if _, err := t.tx.Exec(ctx, `INSERT INTO customers (id, tenant_id, location_id, phone, name, first_name, last_name, notes,
			source, name_source, notes_source) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'counter', $9, $10)`,
			c.ID, tenantID, locationID, c.Phone, c.Name, c.FirstName, c.LastName, c.Notes, c.NameSource, c.NotesSource); err != nil {
			return customer.Customer{}, fmt.Errorf("insert counter customer: %w", err)
		}
	}
	c.TenantID, c.LocationID, c.Source = tenantID, locationID, customer.SourceCounter
	return c, nil
}

func (t counterTx) SetNamesNotes(ctx context.Context, c customer.Customer) error {
	return t.q.UpdateImportedCustomer(ctx, models.UpdateImportedCustomerParams{ID: pgID(c.ID), Name: c.Name, FirstName: c.FirstName,
		LastName: c.LastName, NameSource: c.NameSource, Notes: c.Notes, NotesSource: c.NotesSource})
}

func (t counterTx) AddAddress(ctx context.Context, customerID uuid.UUID, a customer.Address) (customer.Address, error) {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	ins := func(id uuid.UUID) (pgtype.Timestamptz, error) {
		var at pgtype.Timestamptz
		err := t.tx.QueryRow(ctx, `INSERT INTO customer_addresses (id, customer_id, label, line1, line2, city, state, zip, notes, is_default)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT (id) DO NOTHING RETURNING created_at`,
			id, customerID, a.Label, a.Line1, a.Line2, a.City, a.State, a.Zip, a.Notes, a.Default).Scan(&at)
		return at, err
	}
	at, err := ins(a.ID)
	if errors.Is(err, pgx.ErrNoRows) { // the id is taken: a new one
		a.ID = uuid.New()
		at, err = ins(a.ID)
	}
	if err != nil {
		return customer.Address{}, fmt.Errorf("insert counter address: %w", err)
	}
	a.CreatedAt = at.Time
	return a, nil
}

func (t counterTx) UpdateAddress(ctx context.Context, customerID uuid.UUID, a customer.Address) error {
	_, err := t.tx.Exec(ctx, `UPDATE customer_addresses SET label = $3, line1 = $4, line2 = $5, city = $6, state = $7, zip = $8, notes = $9
		WHERE id = $1 AND customer_id = $2`, a.ID, customerID, a.Label, a.Line1, a.Line2, a.City, a.State, a.Zip, a.Notes)
	if err != nil {
		return fmt.Errorf("update counter address: %w", err)
	}
	return nil
}

func (t counterTx) FillAddress(ctx context.Context, a customer.Address) error {
	return t.q.FillCustomerAddress(ctx, models.FillCustomerAddressParams{ID: pgID(a.ID), Label: a.Label, Line2: a.Line2, City: a.City,
		State: a.State, Zip: a.Zip, Notes: a.Notes})
}

// LastChange is when the store's customers last changed: the newest
// updated_at or tombstone (zero when the store has none). Both are indexed
// by (location_id, …).
func (r *CustomerRepository) LastChange(ctx context.Context, locationID uuid.UUID) (time.Time, error) {
	var at pgtype.Timestamptz
	err := r.pool.QueryRow(ctx, `SELECT GREATEST(
		(SELECT max(updated_at) FROM customers WHERE location_id = $1),
		(SELECT max(deleted_at) FROM customer_tombstones WHERE location_id = $1))`, locationID).Scan(&at)
	if err != nil {
		return time.Time{}, err
	}
	return at.Time, nil
}
