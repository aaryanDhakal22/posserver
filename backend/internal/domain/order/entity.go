// Package order is the heart of the domain. The Order aggregate is the single
// canonical representation that flows through main/ — every order, regardless
// of where it originated, is normalised into this shape before any business
// logic, persistence, or dispatch happens.
//
// This file is the AGGREGATE. The DB row shape lives in infrastructure/postgres
// (sqlc-generated). The wire shape that crosses SQS lives in
// internal/casting/<source>/payload.go. The SSE event shape lives in
// transport/sse/envelope.go. Mappers between these and the aggregate live next
// to each transport. The aggregate itself never imports any of them.
package order

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

// ID is the canonical, internal order identifier. UUID v7 — time-ordered,
// index-friendly, globally unique without coordination.
type ID uuid.UUID

// TenantID and LocationID identify the scope an order belongs to. The pair is
// stamped at ingest from the authenticating device (or, for SQS-sourced
// orders, the MessageAttributes published by online/). Every persisted row
// carries this scope so two tenants sharing an upstream vendor order number
// don't collide and a tenant-A device can never read tenant-B's orders.
type TenantID uuid.UUID
type LocationID uuid.UUID

// ---------------------------------------------------------------------------
// Source attribution
// ---------------------------------------------------------------------------

// Source identifies where the order came from. Each source has its own
// caster (anti-corruption layer) in main/internal/casting/<source>.
type Source string

const (
	SourceOnline Source = "online"
	SourceMobile Source = "mobile"
	SourcePOS    Source = "pos"
)

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

type Status string

const (
	StatusReceived  Status = "received"
	StatusValidated Status = "validated"
	StatusAccepted  Status = "accepted"
	StatusPreparing Status = "preparing"
	StatusReady     Status = "ready"
	// StatusOnDelivery: a driver took it out (set by the agent's floor flow).
	StatusOnDelivery Status = "on_delivery"
	StatusCompleted  Status = "completed"
	StatusRejected   Status = "rejected"
	StatusCancelled  Status = "cancelled"
)

var validTransitions = map[Status]map[Status]struct{}{
	StatusReceived:  {StatusValidated: {}, StatusRejected: {}},
	StatusValidated: {StatusAccepted: {}, StatusRejected: {}},
	StatusAccepted:  {StatusPreparing: {}, StatusCancelled: {}},
	StatusPreparing: {StatusReady: {}, StatusCancelled: {}},
	StatusReady:     {StatusCompleted: {}, StatusCancelled: {}},
}

type StatusEvent struct {
	From    Status
	To      Status
	At      time.Time
	ActorID uuid.UUID
	Actor   string
	Reason  string
}

// ---------------------------------------------------------------------------
// Money — int64 minor units, never floats
// ---------------------------------------------------------------------------

type Money int64

type Currency string

type MoneyBreakdown struct {
	Subtotal  Money
	Tax       Money
	Tip       Money
	Fees      []Fee
	Discounts []Discount
	Total     Money
}

type Fee struct {
	Type   string
	Label  string
	Amount Money
}

type Discount struct {
	Type   string
	Code   string
	Label  string
	Amount Money
}

// ---------------------------------------------------------------------------
// Fulfillment
// ---------------------------------------------------------------------------

type FulfillmentType string

const (
	FulfillmentDineIn   FulfillmentType = "dine_in"
	FulfillmentPickup   FulfillmentType = "pickup"
	FulfillmentDelivery FulfillmentType = "delivery"
)

type Fulfillment struct {
	Type       FulfillmentType
	Address    *Address
	PromisedAt *time.Time
	TableID    string
}

type Address struct {
	Line1      string
	Line2      string
	City       string
	State      string
	PostalCode string
	Country    string
	Notes      string
}

type Customer struct {
	DisplayName string
	Email       string
	Phone       string
	ExternalID  string
}

type LineItem struct {
	ID        uuid.UUID
	ParentID  *uuid.UUID
	Position  int
	SKU       string
	Name      string
	Size      string // vendor size (e.g. "12 Medium"), kept out of Name so names match the menu
	Quantity  int
	UnitPrice Money
	LineTotal Money
	Notes     string
	Children  []LineItem

	// Kitchen routing, stamped on top-level items when the order arrives
	// (modifiers/children inherit their parent's). Zero values = not routed
	// (orders that predate routing print as one full receipt).
	MenuItemID *uuid.UUID
	PrinterID  *uuid.UUID
	Routing    Routing
}

// ItemRoute is the kitchen routing for one top-level line item, produced by the
// menu router at ingest and stamped onto the LineItem.
type ItemRoute struct {
	MenuItemID *uuid.UUID
	PrinterID  *uuid.UUID
	Routing    Routing
}

// Routing says how a line item was matched to a kitchen printer.
type Routing string

const (
	RoutingNone       Routing = ""           // not routed (pre-routing orders)
	RoutingMatched    Routing = "matched"    // menu item found + it (or its category) has a printer
	RoutingUnmatched  Routing = "unmatched"  // no menu item with that name
	RoutingUnassigned Routing = "unassigned" // menu item found, but no printer assigned
)

type PaymentStatus string

const (
	PaymentPending    PaymentStatus = "pending"
	PaymentAuthorized PaymentStatus = "authorized"
	PaymentCaptured   PaymentStatus = "captured"
	PaymentFailed     PaymentStatus = "failed"
	PaymentRefunded   PaymentStatus = "refunded"
)

type Payment struct {
	ID         uuid.UUID
	Processor  string // "card" | "cash" | "other" | vendor-specific tag
	Status     PaymentStatus
	Amount     Money
	Token      string
	Last4      string
	AuthCode   string
	CapturedAt *time.Time
}

// ---------------------------------------------------------------------------
// The aggregate
// ---------------------------------------------------------------------------

type Order struct {
	id            ID
	tenantID      TenantID
	locationID    LocationID
	schemaVersion int

	source        Source
	sourceEventID string

	status        Status
	sourceStatus  string
	statusHistory []StatusEvent

	customer    Customer
	fulfillment Fulfillment
	items       []LineItem
	money       MoneyBreakdown
	currency    Currency

	placedAt   time.Time
	receivedAt time.Time
	updatedAt  time.Time

	payments []Payment

	notes string

	sourceMetadata map[string]any

	rawPayload []byte

	// Floor fields mirrored from the agent (see ApplyFloorUpdate).
	kitchenNumber    *int
	estimatedReadyAt *time.Time
	driverName       string
	floorAt          *time.Time // newest applied agent floor event

	pendingEvents []DomainEvent
}

type NewOrderInput struct {
	TenantID      TenantID
	LocationID    LocationID
	Source        Source
	SourceEventID string
	SourceStatus  string

	Customer    Customer
	Fulfillment Fulfillment
	Items       []LineItem
	Money       MoneyBreakdown
	Currency    Currency

	Payments []Payment

	PlacedAt time.Time
	Notes    string

	SourceMetadata map[string]any
	RawPayload     []byte
}

var (
	ErrTenantRequired    = errors.New("order: tenant_id required")
	ErrLocationRequired  = errors.New("order: location_id required")
	ErrSourceRequired    = errors.New("order: source required")
	ErrSourceEventID     = errors.New("order: source_event_id required")
	ErrEmptyItems        = errors.New("order: at least one line item required")
	ErrCurrencyRequired  = errors.New("order: currency required")
	ErrPlacedAtZero      = errors.New("order: placed_at must be set")
	ErrTotalMismatch     = errors.New("order: total does not match breakdown")
	ErrInvalidTransition = errors.New("order: invalid status transition")
	ErrTerminalState     = errors.New("order: order is in a terminal state")
	ErrDuplicate         = errors.New("order: duplicate (tenant_id, source, source_event_id)")
)

func NewOrder(in NewOrderInput) (*Order, error) {
	if uuid.UUID(in.TenantID) == (uuid.UUID{}) {
		return nil, ErrTenantRequired
	}
	if uuid.UUID(in.LocationID) == (uuid.UUID{}) {
		return nil, ErrLocationRequired
	}
	if in.Source == "" {
		return nil, ErrSourceRequired
	}
	if in.SourceEventID == "" {
		return nil, ErrSourceEventID
	}
	if len(in.Items) == 0 {
		return nil, ErrEmptyItems
	}
	if in.Currency == "" {
		return nil, ErrCurrencyRequired
	}
	if in.PlacedAt.IsZero() {
		return nil, ErrPlacedAtZero
	}
	if !moneyReconciles(in.Money) {
		return nil, ErrTotalMismatch
	}

	now := time.Now().UTC()
	rawCopy := append([]byte(nil), in.RawPayload...)
	o := &Order{
		id:             ID(uuid.Must(uuid.NewV7())),
		tenantID:       in.TenantID,
		locationID:     in.LocationID,
		schemaVersion:  1,
		source:         in.Source,
		sourceEventID:  in.SourceEventID,
		status:         StatusReceived,
		sourceStatus:   in.SourceStatus,
		statusHistory:  []StatusEvent{{To: StatusReceived, At: now, Actor: "system"}},
		customer:       in.Customer,
		fulfillment:    in.Fulfillment,
		items:          in.Items,
		money:          in.Money,
		currency:       in.Currency,
		placedAt:       in.PlacedAt.UTC(),
		receivedAt:     now,
		updatedAt:      now,
		notes:          in.Notes,
		sourceMetadata: in.SourceMetadata,
		rawPayload:     rawCopy,
		payments:       append([]Payment(nil), in.Payments...),
	}
	o.recordEvent(EventOrderReceived, o.Snapshot())
	return o, nil
}

type RehydrateInput struct {
	ID             ID
	TenantID       TenantID
	LocationID     LocationID
	SchemaVersion  int
	Source         Source
	SourceEventID  string
	Status         Status
	SourceStatus   string
	StatusHistory  []StatusEvent
	Customer       Customer
	Fulfillment    Fulfillment
	Items          []LineItem
	Money          MoneyBreakdown
	Currency       Currency
	PlacedAt       time.Time
	ReceivedAt     time.Time
	UpdatedAt      time.Time
	Payments       []Payment
	Notes          string
	SourceMetadata map[string]any
	RawPayload     []byte

	KitchenNumber    *int
	EstimatedReadyAt *time.Time
	DriverName       string
	FloorAt          *time.Time
}

func Rehydrate(in RehydrateInput) *Order {
	return &Order{
		id:             in.ID,
		tenantID:       in.TenantID,
		locationID:     in.LocationID,
		schemaVersion:  in.SchemaVersion,
		source:         in.Source,
		sourceEventID:  in.SourceEventID,
		status:         in.Status,
		sourceStatus:   in.SourceStatus,
		statusHistory:  in.StatusHistory,
		customer:       in.Customer,
		fulfillment:    in.Fulfillment,
		items:          in.Items,
		money:          in.Money,
		currency:       in.Currency,
		placedAt:       in.PlacedAt,
		receivedAt:     in.ReceivedAt,
		updatedAt:      in.UpdatedAt,
		payments:       in.Payments,
		notes:          in.Notes,
		sourceMetadata: in.SourceMetadata,
		rawPayload:     in.RawPayload,

		kitchenNumber:    in.KitchenNumber,
		estimatedReadyAt: in.EstimatedReadyAt,
		driverName:       in.DriverName,
		floorAt:          in.FloorAt,
	}
}

// ---------------------------------------------------------------------------
// Read accessors — the aggregate is read-only from the outside
// ---------------------------------------------------------------------------

func (o *Order) ID() ID                       { return o.id }
func (o *Order) TenantID() TenantID           { return o.tenantID }
func (o *Order) LocationID() LocationID       { return o.locationID }
func (o *Order) SchemaVersion() int           { return o.schemaVersion }
func (o *Order) Source() Source               { return o.source }
func (o *Order) SourceEventID() string        { return o.sourceEventID }
func (o *Order) Status() Status               { return o.status }
func (o *Order) SourceStatus() string         { return o.sourceStatus }
func (o *Order) StatusHistory() []StatusEvent { return append([]StatusEvent(nil), o.statusHistory...) }
func (o *Order) Customer() Customer           { return o.customer }
func (o *Order) Fulfillment() Fulfillment     { return o.fulfillment }
func (o *Order) Items() []LineItem            { return append([]LineItem(nil), o.items...) }
func (o *Order) Money() MoneyBreakdown        { return o.money }
func (o *Order) Currency() Currency           { return o.currency }
func (o *Order) PlacedAt() time.Time          { return o.placedAt }
func (o *Order) ReceivedAt() time.Time        { return o.receivedAt }
func (o *Order) UpdatedAt() time.Time         { return o.updatedAt }
func (o *Order) Payments() []Payment          { return append([]Payment(nil), o.payments...) }
func (o *Order) Notes() string                { return o.notes }
func (o *Order) SourceMetadata() map[string]any {
	if o.sourceMetadata == nil {
		return nil
	}
	cp := make(map[string]any, len(o.sourceMetadata))
	for k, v := range o.sourceMetadata {
		cp[k] = v
	}
	return cp
}
func (o *Order) RawPayload() []byte           { return append([]byte(nil), o.rawPayload...) }
func (o *Order) KitchenNumber() *int          { return o.kitchenNumber }
func (o *Order) EstimatedReadyAt() *time.Time { return o.estimatedReadyAt }
func (o *Order) DriverName() string           { return o.driverName }
func (o *Order) FloorAt() *time.Time          { return o.floorAt }
func (o *Order) IsTerminal() bool {
	return o.status == StatusCompleted || o.status == StatusRejected || o.status == StatusCancelled
}

func (o *Order) Transition(to Status, actor string, actorID uuid.UUID, reason string) error {
	if o.IsTerminal() {
		return ErrTerminalState
	}
	allowed, ok := validTransitions[o.status]
	if !ok {
		return ErrInvalidTransition
	}
	if _, ok := allowed[to]; !ok {
		return ErrInvalidTransition
	}
	if to == StatusCancelled && reason == "" {
		return errors.New("order: cancellation reason required")
	}
	now := time.Now().UTC()
	from := o.status
	o.statusHistory = append(o.statusHistory, StatusEvent{
		From:    from,
		To:      to,
		At:      now,
		Actor:   actor,
		ActorID: actorID,
		Reason:  reason,
	})
	o.status = to
	o.updatedAt = now
	o.recordEvent(EventOrderStatusChanged, StatusChangedPayload{
		From:     string(from),
		To:       string(to),
		Actor:    actor,
		Reason:   reason,
		Snapshot: o.Snapshot(),
	})
	return nil
}

func (o *Order) AttachPayment(p Payment) {
	o.payments = append(o.payments, p)
	o.updatedAt = time.Now().UTC()
}

func moneyReconciles(m MoneyBreakdown) bool {
	var fees Money
	for _, f := range m.Fees {
		fees += f.Amount
	}
	var discounts Money
	for _, d := range m.Discounts {
		discounts += d.Amount
	}
	expected := m.Subtotal + m.Tax + m.Tip + fees - discounts
	diff := m.Total - expected
	if diff < 0 {
		diff = -diff
	}
	return diff <= 1
}
