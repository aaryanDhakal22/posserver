package repositories

import (
	"context"
	"time"

	"quiccpos/main/internal/domain/customer"
	"quiccpos/main/internal/infra/database/models"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CustomerRepository persists stores' customer accounts (weborder stage 2).
type CustomerRepository struct {
	pool *pgxpool.Pool
}

func NewCustomerRepository(pool *pgxpool.Pool) *CustomerRepository {
	return &CustomerRepository{pool: pool}
}

func customerOf(r models.Customer) customer.Customer {
	return customer.Customer{ID: idOf(r.ID), TenantID: idOf(r.TenantID), LocationID: idOf(r.LocationID), Phone: r.Phone,
		Name: r.Name, Email: r.Email, SquareCustomerID: r.SquareCustomerID, CreatedAt: r.CreatedAt.Time,
		FirstName: r.FirstName, LastName: r.LastName, Notes: r.Notes, Source: r.Source, NameSource: r.NameSource,
		NotesSource: r.NotesSource, UpdatedAt: r.UpdatedAt.Time}
}

func addressOf(r models.CustomerAddress) customer.Address {
	return customer.Address{ID: idOf(r.ID), Label: r.Label, Line1: r.Line1, Line2: r.Line2, City: r.City, State: r.State, Zip: r.Zip,
		Notes: r.Notes, Default: r.IsDefault, CreatedAt: r.CreatedAt.Time}
}

func cardOf(r models.CustomerCard) customer.Card {
	return customer.Card{ID: idOf(r.ID), Gateway: r.Gateway, Ref: r.CardRef, Brand: r.Brand, Last4: r.Last4,
		ExpMonth: int(r.ExpMonth), ExpYear: int(r.ExpYear), CreatedAt: r.CreatedAt.Time}
}

// SignIn finds or creates the store's account for a verified phone, joins
// that phone's guest web orders to it and opens a session, in one tx.
// created reports a new account.
func (r *CustomerRepository) SignIn(ctx context.Context, tenantID, locationID uuid.UUID, phone, name, tokenHash string,
	expires time.Time) (customer.Customer, bool, int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return customer.Customer{}, false, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := models.New(tx)
	first, last := customer.SplitName(name)
	row, err := q.UpsertCustomerByPhone(ctx, models.UpsertCustomerByPhoneParams{TenantID: pgID(tenantID), LocationID: pgID(locationID),
		Phone: phone, Name: name, FirstName: first, LastName: last})
	if err != nil {
		return customer.Customer{}, false, 0, err
	}
	c := customerOf(models.Customer{ID: row.ID, TenantID: row.TenantID, LocationID: row.LocationID, Phone: row.Phone, Name: row.Name,
		Email: row.Email, SquareCustomerID: row.SquareCustomerID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		LastSeenAt: row.LastSeenAt, FirstName: row.FirstName, LastName: row.LastName, Notes: row.Notes, Source: row.Source,
		NameSource: row.NameSource, NotesSource: row.NotesSource})
	joined, err := q.JoinGuestOrders(ctx, models.JoinGuestOrdersParams{LocationID: pgID(locationID), CustomerPhone: phone, CustomerID: pgID(c.ID)})
	if err != nil {
		return c, false, 0, err
	}
	if err := q.InsertCustomerSession(ctx, models.InsertCustomerSessionParams{TokenHash: tokenHash, CustomerID: pgID(c.ID),
		LocationID: pgID(locationID), ExpiresAt: ts(expires)}); err != nil {
		return c, false, 0, err
	}
	return c, row.Created, joined, tx.Commit(ctx)
}

// BySession is a live session's customer, renewing the session to renewTo.
func (r *CustomerRepository) BySession(ctx context.Context, tokenHash string, locationID uuid.UUID, renewTo time.Time) (customer.Customer, error) {
	row, err := models.New(r.pool).CustomerBySession(ctx, models.CustomerBySessionParams{TokenHash: tokenHash, LocationID: pgID(locationID),
		Column3: ts(renewTo)})
	if err != nil {
		return customer.Customer{}, err
	}
	return customerOf(models.Customer(row)), nil
}

func (r *CustomerRepository) EndSession(ctx context.Context, tokenHash string) error {
	return models.New(r.pool).DeleteCustomerSession(ctx, tokenHash)
}

func (r *CustomerRepository) PruneSessions(ctx context.Context) (int64, error) {
	return models.New(r.pool).PruneCustomerSessions(ctx)
}

func (r *CustomerRepository) Get(ctx context.Context, id uuid.UUID) (customer.Customer, error) {
	row, err := models.New(r.pool).GetCustomer(ctx, pgID(id))
	if err != nil {
		return customer.Customer{}, err
	}
	return customerOf(row), nil
}

// UpdateProfile saves the website's profile; nameSet = the customer sent a
// name (it becomes theirs: first + last follow it, name_source web).
func (r *CustomerRepository) UpdateProfile(ctx context.Context, id uuid.UUID, name, email string, nameSet bool) (customer.Customer, error) {
	first, last := customer.SplitName(name)
	row, err := models.New(r.pool).UpdateCustomerProfile(ctx, models.UpdateCustomerProfileParams{ID: pgID(id), Name: name, Email: email,
		FirstName: first, LastName: last, NameSet: nameSet})
	if err != nil {
		return customer.Customer{}, err
	}
	return customerOf(row), nil
}

func (r *CustomerRepository) SetSquareID(ctx context.Context, id uuid.UUID, squareID string) error {
	return models.New(r.pool).SetCustomerSquareID(ctx, models.SetCustomerSquareIDParams{ID: pgID(id), SquareCustomerID: squareID})
}

// Delete removes the account (addresses, sessions and cards go with it; its
// orders stay, unlinked).
func (r *CustomerRepository) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := models.New(r.pool).DeleteCustomer(ctx, pgID(id))
	return err
}

func (r *CustomerRepository) Addresses(ctx context.Context, customerID uuid.UUID) ([]customer.Address, error) {
	rows, err := models.New(r.pool).ListCustomerAddresses(ctx, pgID(customerID))
	if err != nil {
		return nil, err
	}
	out := make([]customer.Address, 0, len(rows))
	for _, x := range rows {
		out = append(out, addressOf(x))
	}
	return out, nil
}

// SaveAddress adds (id nil) or replaces an address; a default address takes
// over from the old default. The account's first address is its default.
func (r *CustomerRepository) SaveAddress(ctx context.Context, customerID uuid.UUID, id *uuid.UUID, a customer.Address) (customer.Address, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return customer.Address{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := models.New(tx)
	if id == nil {
		n, err := q.CountCustomerAddresses(ctx, pgID(customerID))
		if err != nil {
			return customer.Address{}, err
		}
		if n >= 20 {
			return customer.Address{}, customer.ErrMaxAddress
		}
		a.Default = a.Default || n == 0
	}
	if a.Default {
		if err := q.ClearCustomerDefaultAddress(ctx, pgID(customerID)); err != nil {
			return customer.Address{}, err
		}
	}
	var row models.CustomerAddress
	if id == nil {
		row, err = q.InsertCustomerAddress(ctx, models.InsertCustomerAddressParams{CustomerID: pgID(customerID), Label: a.Label,
			Line1: a.Line1, Line2: a.Line2, City: a.City, State: a.State, Zip: a.Zip, Notes: a.Notes, IsDefault: a.Default})
	} else {
		row, err = q.UpdateCustomerAddress(ctx, models.UpdateCustomerAddressParams{ID: pgID(*id), CustomerID: pgID(customerID), Label: a.Label,
			Line1: a.Line1, Line2: a.Line2, City: a.City, State: a.State, Zip: a.Zip, Notes: a.Notes, IsDefault: a.Default})
	}
	if err != nil {
		return customer.Address{}, err
	}
	return addressOf(row), tx.Commit(ctx)
}

func (r *CustomerRepository) DeleteAddress(ctx context.Context, customerID, id uuid.UUID) (bool, error) {
	n, err := models.New(r.pool).DeleteCustomerAddress(ctx, models.DeleteCustomerAddressParams{ID: pgID(id), CustomerID: pgID(customerID)})
	return n > 0, err
}

func (r *CustomerRepository) Cards(ctx context.Context, customerID uuid.UUID) ([]customer.Card, error) {
	rows, err := models.New(r.pool).ListCustomerCards(ctx, pgID(customerID))
	if err != nil {
		return nil, err
	}
	out := make([]customer.Card, 0, len(rows))
	for _, x := range rows {
		out = append(out, cardOf(x))
	}
	return out, nil
}

func (r *CustomerRepository) Card(ctx context.Context, customerID, id uuid.UUID) (customer.Card, error) {
	row, err := models.New(r.pool).GetCustomerCard(ctx, models.GetCustomerCardParams{ID: pgID(id), CustomerID: pgID(customerID)})
	if err != nil {
		return customer.Card{}, err
	}
	return cardOf(row), nil
}

func (r *CustomerRepository) AddCard(ctx context.Context, customerID uuid.UUID, c customer.Card) (customer.Card, error) {
	row, err := models.New(r.pool).InsertCustomerCard(ctx, models.InsertCustomerCardParams{CustomerID: pgID(customerID), Gateway: c.Gateway,
		CardRef: c.Ref, Brand: c.Brand, Last4: c.Last4, ExpMonth: int32(c.ExpMonth), ExpYear: int32(c.ExpYear)})
	if err != nil {
		return customer.Card{}, err
	}
	return cardOf(row), nil
}

func (r *CustomerRepository) DeleteCard(ctx context.Context, customerID, id uuid.UUID) (bool, error) {
	n, err := models.New(r.pool).DeleteCustomerCard(ctx, models.DeleteCustomerCardParams{ID: pgID(id), CustomerID: pgID(customerID)})
	return n > 0, err
}

// Orders are the account's channel order links, newest first.
func (r *CustomerRepository) Orders(ctx context.Context, customerID uuid.UUID, before time.Time, limit int) ([]ChannelOrder, error) {
	rows, err := models.New(r.pool).ListCustomerOrders(ctx, models.ListCustomerOrdersParams{CustomerID: pgID(customerID),
		CreatedAt: pgtype.Timestamptz{Time: before, Valid: true}, Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]ChannelOrder, 0, len(rows))
	for _, x := range rows {
		out = append(out, channelOrderOf(x))
	}
	return out, nil
}

// Order is one of the account's order links (pgx.ErrNoRows = not theirs).
func (r *CustomerRepository) Order(ctx context.Context, customerID, orderID uuid.UUID) (ChannelOrder, error) {
	row, err := models.New(r.pool).GetChannelOrderForCustomer(ctx, models.GetChannelOrderForCustomerParams{OrderID: pgID(orderID),
		CustomerID: pgID(customerID)})
	if err != nil {
		return ChannelOrder{}, err
	}
	return channelOrderOf(row), nil
}

// ReplyEmail is the store's email for receipts' Reply-To ("" = none).
func (r *CustomerRepository) ReplyEmail(ctx context.Context, locationID uuid.UUID) (string, error) {
	e, err := models.New(r.pool).GetSiteReplyEmail(ctx, pgID(locationID))
	if err != nil {
		return "", nil // no site settings yet
	}
	return e, nil
}

func (r *CustomerRepository) SetReplyEmail(ctx context.Context, tenantID, locationID uuid.UUID, email string) error {
	return models.New(r.pool).SetSiteReplyEmail(ctx, models.SetSiteReplyEmailParams{LocationID: pgID(locationID), TenantID: pgID(tenantID),
		ReplyEmail: email})
}
