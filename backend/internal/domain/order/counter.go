package order

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Counter orders (TERMINAL-PLAN.md 0d): the store's Pi takes an order at the
// counter terminal, numbers it, prints it and syncs it here as the floor
// event order.placed with a Snapshot-shaped body. main keeps it under the
// Pi's id, never sends it back to the Pi, and treats it as any order after.

// SourceCounter is a counter terminal order (from the Pi).
const SourceCounter Source = "counter"

// FulfillmentCounter is a walk-in sale at the counter.
const FulfillmentCounter FulfillmentType = "counter"

// Counter payment methods.
var CounterMethods = map[string]bool{"cash": true, "card": true, "square_app": true, "manual_card": true, "comp": true, "other": true}

// Limits on a counter order.
const (
	MaxCounterLines    = 200
	MaxCounterChildren = 60
	MaxCounterPayments = 50
	MaxCounterText     = 500
	MaxPaymentCents    = 10_000_000
)

var ErrBadCounterOrder = errors.New("bad counter order")

func badCounter(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadCounterOrder, fmt.Sprintf(format, a...))
}

// CounterSnapshot is order.placed's snapshot: the Snapshot shape plus who
// took it and where.
type CounterSnapshot struct {
	Snapshot
	StaffID  string `json:"staff_id"`
	DeviceID string `json:"device_id"`
	// Payments as the Pi records them (Snapshot.Payments is the summary
	// shape; a counter snapshot carries the full payment).
	CounterPayments []CounterPayment `json:"payments"`
	// CustomerID: the store customer the order is for (terminal 1d; the Pi's
	// id of it), kept in source_metadata.customer_id.
	CustomerID string `json:"customer_id"`
}

// CounterPayment is one payment (or, negative, refund) taken at the counter.
type CounterPayment struct {
	ID          string    `json:"id"`
	Method      string    `json:"method"`
	AmountCents int64     `json:"amount_cents"`
	TipCents    int64     `json:"tip_cents"`
	Last4       string    `json:"last4"`
	AuthCode    string    `json:"auth_code"`
	Ref         string    `json:"ref"`
	At          time.Time `json:"at"`
	StaffID     string    `json:"staff_id"`
}

// CheckedPayment is a validated CounterPayment.
type CheckedPayment struct {
	ID       uuid.UUID
	Method   string
	Status   PaymentStatus // captured, or refunded for a negative amount
	Amount   Money
	Tip      Money
	Last4    string
	AuthCode string
	Ref      string
	At       time.Time
	StaffID  *uuid.UUID
}

func optID(s, what string) (*uuid.UUID, error) {
	if s == "" {
		return nil, nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil, badCounter("%s is not a uuid", what)
	}
	return &id, nil
}

func short(s string, max int, what string) (string, error) {
	if utf8.RuneCountInString(s) > max {
		return "", badCounter("%s is too long (max %d)", what, max)
	}
	return s, nil
}

// Check validates a counter payment.
func (p CounterPayment) Check() (CheckedPayment, error) {
	id, err := uuid.Parse(p.ID)
	if err != nil {
		return CheckedPayment{}, badCounter("payment id is not a uuid")
	}
	if !CounterMethods[p.Method] {
		return CheckedPayment{}, badCounter("payment method %q (cash, card, square_app, manual_card, comp, other)", p.Method)
	}
	if p.AmountCents == 0 || p.AmountCents > MaxPaymentCents || p.AmountCents < -MaxPaymentCents {
		return CheckedPayment{}, badCounter("payment amount_cents must be non-zero, within ±%d", MaxPaymentCents)
	}
	if p.TipCents < 0 || p.TipCents > MaxPaymentCents {
		return CheckedPayment{}, badCounter("payment tip_cents out of range")
	}
	staffID, err := optID(p.StaffID, "payment staff_id")
	if err != nil {
		return CheckedPayment{}, err
	}
	for _, f := range []struct{ v, what string }{{p.Last4, "last4"}, {p.AuthCode, "auth_code"}, {p.Ref, "ref"}} {
		if _, err := short(f.v, 100, "payment "+f.what); err != nil {
			return CheckedPayment{}, err
		}
	}
	at := p.At.UTC()
	if p.At.IsZero() {
		at = time.Now().UTC()
	}
	st := PaymentCaptured
	if p.AmountCents < 0 {
		st = PaymentRefunded
	}
	return CheckedPayment{ID: id, Method: p.Method, Status: st, Amount: Money(p.AmountCents), Tip: Money(p.TipCents),
		Last4: p.Last4, AuthCode: p.AuthCode, Ref: p.Ref, At: at, StaffID: staffID}, nil
}

// CounterOrder is a counter snapshot made into an order (+ its payments and
// the floor state the Pi gave it, to apply after the insert).
type CounterOrder struct {
	Order    *Order
	Payments []CheckedPayment
	Floor    FloorUpdate
}

// NewCounterOrder builds the order the Pi placed, in the Pi's scope (the
// snapshot's tenant / location are ignored), under the Pi's id.
func NewCounterOrder(sc Scope, s CounterSnapshot, raw []byte) (*CounterOrder, error) {
	id, err := uuid.Parse(s.ID)
	if err != nil || id == uuid.Nil {
		return nil, badCounter("id is not a uuid")
	}
	if s.Source != "" && s.Source != string(SourceCounter) {
		return nil, badCounter("source must be counter")
	}
	sev := strings.TrimSpace(s.SourceEventID)
	if sev == "" {
		sev = id.String()
	}
	if len(sev) > 200 {
		return nil, badCounter("source_event_id is too long")
	}
	state := s.Status
	if state == "" {
		state = "accepted"
	}
	status, ok := FloorStatus(state)
	if !ok || status == StatusScheduled {
		return nil, badCounter("status %q", s.Status)
	}
	ft := FulfillmentType(s.Fulfillment.Type)
	if ft != FulfillmentCounter && ft != FulfillmentPickup && ft != FulfillmentDelivery {
		return nil, badCounter("fulfillment type must be counter, pickup or delivery")
	}
	if len(s.Items) == 0 || len(s.Items) > MaxCounterLines {
		return nil, badCounter("1–%d items", MaxCounterLines)
	}
	if len(s.CounterPayments) > MaxCounterPayments {
		return nil, badCounter("at most %d payments", MaxCounterPayments)
	}
	if s.PlacedAt.IsZero() {
		return nil, badCounter("placed_at is required")
	}
	staffID, err := optID(s.StaffID, "staff_id")
	if err != nil {
		return nil, err
	}
	if _, err := short(s.DeviceID, 64, "device_id"); err != nil {
		return nil, err
	}
	if _, err := short(s.Notes, MaxCounterText, "notes"); err != nil {
		return nil, err
	}
	items, err := counterItems(s.Items, 0)
	if err != nil {
		return nil, err
	}
	pays := make([]CheckedPayment, 0, len(s.CounterPayments))
	for _, p := range s.CounterPayments {
		cp, err := p.Check()
		if err != nil {
			return nil, err
		}
		pays = append(pays, cp)
	}
	m := MoneyBreakdown{Subtotal: Money(s.Money.Subtotal), Tax: Money(s.Money.Tax), Tip: Money(s.Money.Tip), Total: Money(s.Money.Total)}
	for _, f := range s.Money.Fees {
		fee := Fee{Type: f.Type, Label: f.Label, Amount: Money(f.Amount)}
		if f.Type == "delivery" && f.DriverCents > 0 && f.DriverCents <= f.Amount {
			fee.DriverCents = Money(f.DriverCents)
		}
		m.Fees = append(m.Fees, fee)
	}
	for _, d := range s.Money.Discounts {
		m.Discounts = append(m.Discounts, Discount{Type: d.Type, Code: d.Code, Label: d.Label, Amount: Money(d.Amount)})
	}
	if !moneyReconciles(m) {
		return nil, badCounter("money doesn't add up (total = subtotal + tax + tip + fees − discounts)")
	}
	var addr *Address
	if a := s.Fulfillment.Address; a != nil {
		addr = &Address{Line1: a.Line1, Line2: a.Line2, City: a.City, State: a.State, PostalCode: a.PostalCode, Country: a.Country, Notes: a.Notes}
	}
	cur := Currency(s.Currency)
	if cur == "" {
		cur = "USD"
	}
	meta := map[string]any{"client_key": s.SourceEventID}
	if staffID != nil {
		meta["staff_id"] = staffID.String()
	}
	if s.DeviceID != "" {
		meta["device_id"] = s.DeviceID
	}
	if cid, err := uuid.Parse(s.CustomerID); err == nil {
		meta["customer_id"] = cid.String()
	}
	o, err := NewOrder(NewOrderInput{
		ID: ID(id), TenantID: sc.TenantID, LocationID: sc.LocationID, Source: SourceCounter, SourceEventID: sev,
		Customer:    Customer{DisplayName: s.Customer.DisplayName, Email: s.Customer.Email, Phone: s.Customer.Phone},
		Fulfillment: Fulfillment{Type: ft, Address: addr, PromisedAt: s.Fulfillment.PromisedAt},
		Items:       items, Money: m, Currency: cur, PlacedAt: s.PlacedAt, Notes: s.Notes,
		SourceMetadata: meta, RawPayload: raw,
	})
	if err != nil {
		return nil, badCounter("%v", err)
	}
	return &CounterOrder{Order: o, Payments: pays, Floor: FloorUpdate{Status: status, KitchenNumber: s.KitchenNumber,
		EstimatedReadyAt: s.EstimatedReadyAt, DriverName: s.DriverName, At: s.PlacedAt}}, nil
}

var counterPortions = map[string]bool{"": true, "first": true, "second": true}
var counterModes = map[string]bool{"": true, "add": true, "only": true, "no": true, "specialty": true}
var counterRouting = map[string]bool{"": true, string(RoutingMatched): true, string(RoutingUnmatched): true, string(RoutingUnassigned): true}

func counterItems(in []LineItemDTO, depth int) ([]LineItem, error) {
	if depth > 2 {
		return nil, badCounter("line items nest at most 2 deep")
	}
	out := make([]LineItem, 0, len(in))
	for i, d := range in {
		if strings.TrimSpace(d.Name) == "" {
			return nil, badCounter("item %d has no name", i)
		}
		if d.Quantity < 1 || d.Quantity > 999 {
			return nil, badCounter("item %q quantity 1–999", d.Name)
		}
		if len(d.Children) > MaxCounterChildren {
			return nil, badCounter("item %q has too many modifiers", d.Name)
		}
		if !counterPortions[d.Portion] || !counterModes[d.Mode] || !counterRouting[d.Routing] {
			return nil, badCounter("item %q: bad portion, mode or routing", d.Name)
		}
		for _, f := range []struct{ v, what string }{{d.Name, "name"}, {d.Notes, "notes"}, {d.Size, "size"}, {d.SKU, "sku"}} {
			if _, err := short(f.v, MaxCounterText, "item "+f.what); err != nil {
				return nil, err
			}
		}
		id := uuid.Must(uuid.NewV7())
		if d.ID != "" {
			p, err := uuid.Parse(d.ID)
			if err != nil {
				return nil, badCounter("item %q id is not a uuid", d.Name)
			}
			id = p
		}
		menuItem, err := optID(d.MenuItemID, "menu_item_id")
		if err != nil {
			return nil, err
		}
		printer, err := optID(d.PrinterID, "printer_id")
		if err != nil {
			return nil, err
		}
		kids, err := counterItems(d.Children, depth+1)
		if err != nil {
			return nil, err
		}
		pos := d.Position
		if pos == 0 {
			pos = i
		}
		out = append(out, LineItem{ID: id, Position: pos, SKU: d.SKU, Name: d.Name, Size: d.Size, Quantity: d.Quantity,
			UnitPrice: Money(d.UnitPrice), LineTotal: Money(d.LineTotal), Notes: d.Notes, Children: kids,
			Portion: d.Portion, Mode: d.Mode, MenuItemID: menuItem, PrinterID: printer, Routing: Routing(d.Routing)})
	}
	return out, nil
}

// ApplyVoid cancels a counter order the terminal voided, in any state that
// isn't cancelled / rejected already (a completed sale too: its refund is a
// separate negative payment). FloorStale when it is already cancelled.
func (o *Order) ApplyVoid(reason string, staffID *uuid.UUID, at time.Time) string {
	if o.status == StatusCancelled || o.status == StatusRejected {
		return FloorStale
	}
	at = at.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	actorID := uuid.Nil
	if staffID != nil {
		actorID = *staffID
	}
	from := o.status
	why := "void: " + reason
	o.statusHistory = append(o.statusHistory, StatusEvent{From: from, To: StatusCancelled, At: at, Actor: "staff", ActorID: actorID, Reason: why})
	o.status = StatusCancelled
	o.updatedAt = time.Now().UTC()
	o.recordEvent(EventOrderStatusChanged, StatusChangedPayload{
		From: string(from), To: string(StatusCancelled), Actor: "staff", Reason: why, Snapshot: o.Snapshot(),
	})
	return FloorApplied
}
