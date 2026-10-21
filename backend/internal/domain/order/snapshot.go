package order

import (
	"time"

	"github.com/google/uuid"
)

// Snapshot is the canonical, JSON-serializable representation of an Order
// at a point in time. It is the wire shape used by:
//
//   - the outbox (DomainEvent payloads carry a Snapshot at event time so
//     handlers act on the order *as it was* when the event fired, not
//     whatever it has become by the time the dispatcher gets there),
//   - the SSE broadcast channel (the OrderEvent struct in transport/sse
//     embeds this and adds trace-context fields),
//   - the HTTP read endpoints (GET /orders, /orders/:id, /orders/by-source/…).
//
// Keeping it in domain/order means there is exactly one shape for "an order
// on the wire" — handlers, transports, and tests all speak the same thing.
type Snapshot struct {
	ID            string           `json:"id"`
	TenantID      string           `json:"tenant_id"`
	LocationID    string           `json:"location_id"`
	Source        string           `json:"source"`
	SourceEventID string           `json:"source_event_id"`
	Status        string           `json:"status"`
	Currency      string           `json:"currency"`
	Money         MoneyDTO         `json:"money"`
	Customer      CustomerDTO      `json:"customer"`
	Fulfillment   FulfillmentDTO   `json:"fulfillment"`
	Items         []LineItemDTO    `json:"items"`
	Payments      []PaymentSummary `json:"payments,omitempty"`
	Notes         string           `json:"notes,omitempty"`
	PlacedAt      time.Time        `json:"placed_at"`
	ReceivedAt    time.Time        `json:"received_at"`
	SourceMeta    map[string]any   `json:"source_metadata,omitempty"`

	// Floor fields mirrored from the restaurant's agent (absent until it
	// accepts the order). Future customer order tracking reads these.
	KitchenNumber    *int       `json:"kitchen_order_number,omitempty"`
	EstimatedReadyAt *time.Time `json:"estimated_ready_at,omitempty"`
	DriverName       string     `json:"driver_name,omitempty"`
}

// PaymentSummary is the wire-level slice of a Payment that downstream
// consumers (agent receipt builder, mobile UI, Pushover sound logic) actually
// need. Source-specific fields the canonical doesn't promote — token, full
// card number, processor-internal IDs — stay in source_metadata.
//
// Empty Payments slice signals an unpaid / cash-on-delivery order to the
// agent's sound + receipt logic.
type PaymentSummary struct {
	Method      string `json:"method"` // "card" | "cash" | "other"
	AmountCents int64  `json:"amount_cents"`
	Last4       string `json:"last4,omitempty"` // last 4 of card number; "" for non-card
	AuthCode    string `json:"auth_code,omitempty"`
}

type MoneyDTO struct {
	Subtotal  int64         `json:"subtotal_cents"`
	Tax       int64         `json:"tax_cents"`
	Tip       int64         `json:"tip_cents"`
	Fees      []FeeDTO      `json:"fees,omitempty"`
	Discounts []DiscountDTO `json:"discounts,omitempty"`
	Total     int64         `json:"total_cents"`
}

type FeeDTO struct {
	Type   string `json:"type"`
	Label  string `json:"label,omitempty"`
	Amount int64  `json:"amount_cents"`
}

type DiscountDTO struct {
	Type   string `json:"type"`
	Code   string `json:"code,omitempty"`
	Label  string `json:"label,omitempty"`
	Amount int64  `json:"amount_cents"`
}

type CustomerDTO struct {
	DisplayName string `json:"display_name,omitempty"`
	Email       string `json:"email,omitempty"`
	Phone       string `json:"phone,omitempty"`
}

type FulfillmentDTO struct {
	Type       string      `json:"type"`
	Address    *AddressDTO `json:"address,omitempty"`
	PromisedAt *time.Time  `json:"promised_at,omitempty"`
	TableID    string      `json:"table_id,omitempty"`
}

type AddressDTO struct {
	Line1      string `json:"line1,omitempty"`
	Line2      string `json:"line2,omitempty"`
	City       string `json:"city,omitempty"`
	State      string `json:"state,omitempty"`
	PostalCode string `json:"postal_code,omitempty"`
	Country    string `json:"country,omitempty"`
	Notes      string `json:"notes,omitempty"`
}

type LineItemDTO struct {
	ID        string        `json:"id"`
	Position  int           `json:"position"`
	SKU       string        `json:"sku,omitempty"`
	Name      string        `json:"name"`
	Size      string        `json:"size,omitempty"`
	Quantity  int           `json:"quantity"`
	UnitPrice int64         `json:"unit_price_cents"`
	LineTotal int64         `json:"line_total_cents"`
	Notes     string        `json:"notes,omitempty"`
	Children  []LineItemDTO `json:"children,omitempty"`
	// Kitchen routing (top-level items): the matched menu item, the station
	// printer to print on, and how it was matched (matched|unmatched|unassigned).
	MenuItemID string `json:"menu_item_id,omitempty"`
	PrinterID  string `json:"printer_id,omitempty"`
	Routing    string `json:"routing,omitempty"`
}

// StatusChangedPayload is the outbox payload for EventOrderStatusChanged.
// Carries the from/to + post-transition Snapshot so handlers see exactly
// what the order looked like immediately after the transition fired.
type StatusChangedPayload struct {
	From     string   `json:"from"`
	To       string   `json:"to"`
	Actor    string   `json:"actor"`
	Reason   string   `json:"reason,omitempty"`
	Snapshot Snapshot `json:"snapshot"`
}

// Snapshot returns the wire-shape projection of the aggregate. The
// returned value is independent of the aggregate — mutating it is safe.
//
// Used at event-emit time (via recordEvent) so the outbox payload captures
// the aggregate's state right then, and reused by the HTTP read endpoints
// for outbound JSON.
func (o *Order) Snapshot() Snapshot {
	return Snapshot{
		ID:            uuid.UUID(o.id).String(),
		TenantID:      uuid.UUID(o.tenantID).String(),
		LocationID:    uuid.UUID(o.locationID).String(),
		Source:        string(o.source),
		SourceEventID: o.sourceEventID,
		Status:        string(o.status),
		Currency:      string(o.currency),
		Money:         toMoneyDTO(o.money),
		Customer:      toCustomerDTO(o.customer),
		Fulfillment:   toFulfillmentDTO(o.fulfillment),
		Items:         toItemsDTO(o.items),
		Payments:      toPaymentSummariesDTO(o.payments),
		Notes:         o.notes,
		PlacedAt:      o.placedAt,
		ReceivedAt:    o.receivedAt,
		SourceMeta:    cloneMeta(o.sourceMetadata),

		KitchenNumber:    o.kitchenNumber,
		EstimatedReadyAt: o.estimatedReadyAt,
		DriverName:       o.driverName,
	}
}

// toPaymentSummariesDTO projects the rich domain Payments slice down to the
// wire-shape PaymentSummary. Empty input → nil output so the json omitempty
// tag drops the field entirely for cash/unpaid orders.
func toPaymentSummariesDTO(payments []Payment) []PaymentSummary {
	if len(payments) == 0 {
		return nil
	}
	out := make([]PaymentSummary, 0, len(payments))
	for _, p := range payments {
		method := p.Processor
		if method == "" {
			method = "other"
		}
		out = append(out, PaymentSummary{
			Method:      method,
			AmountCents: int64(p.Amount),
			Last4:       p.Last4,
			AuthCode:    p.AuthCode,
		})
	}
	return out
}

func toMoneyDTO(m MoneyBreakdown) MoneyDTO {
	fees := make([]FeeDTO, 0, len(m.Fees))
	for _, f := range m.Fees {
		fees = append(fees, FeeDTO{Type: f.Type, Label: f.Label, Amount: int64(f.Amount)})
	}
	discounts := make([]DiscountDTO, 0, len(m.Discounts))
	for _, d := range m.Discounts {
		discounts = append(discounts, DiscountDTO{Type: d.Type, Code: d.Code, Label: d.Label, Amount: int64(d.Amount)})
	}
	return MoneyDTO{
		Subtotal:  int64(m.Subtotal),
		Tax:       int64(m.Tax),
		Tip:       int64(m.Tip),
		Fees:      fees,
		Discounts: discounts,
		Total:     int64(m.Total),
	}
}

func toCustomerDTO(c Customer) CustomerDTO {
	return CustomerDTO{
		DisplayName: c.DisplayName,
		Email:       c.Email,
		Phone:       c.Phone,
	}
}

func toFulfillmentDTO(f Fulfillment) FulfillmentDTO {
	out := FulfillmentDTO{
		Type:       string(f.Type),
		PromisedAt: f.PromisedAt,
		TableID:    f.TableID,
	}
	if f.Address != nil {
		out.Address = &AddressDTO{
			Line1:      f.Address.Line1,
			Line2:      f.Address.Line2,
			City:       f.Address.City,
			State:      f.Address.State,
			PostalCode: f.Address.PostalCode,
			Country:    f.Address.Country,
			Notes:      f.Address.Notes,
		}
	}
	return out
}

func toItemsDTO(items []LineItem) []LineItemDTO {
	out := make([]LineItemDTO, 0, len(items))
	for _, it := range items {
		dto := LineItemDTO{
			ID:        it.ID.String(),
			Position:  it.Position,
			SKU:       it.SKU,
			Name:      it.Name,
			Size:      it.Size,
			Quantity:  it.Quantity,
			UnitPrice: int64(it.UnitPrice),
			LineTotal: int64(it.LineTotal),
			Notes:     it.Notes,
			Children:  toItemsDTO(it.Children),
			Routing:   string(it.Routing),
		}
		if it.MenuItemID != nil {
			dto.MenuItemID = it.MenuItemID.String()
		}
		if it.PrinterID != nil {
			dto.PrinterID = it.PrinterID.String()
		}
		out = append(out, dto)
	}
	return out
}

func cloneMeta(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
