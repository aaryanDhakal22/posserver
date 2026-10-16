// Package online is the anti-corruption layer for orders that arrive via the
// `online/` Lightsail relay (the external POS vendor → SQS → main/) path.
//
// It owns:
//   - the on-the-wire payload shape that the vendor JSON deserializes into
//     (this file), and
//   - the Caster that maps that shape into the canonical Order aggregate
//     (caster.go).
//
// Nothing outside this package should reference Payload — once Cast returns,
// the rest of main/ deals only in *order.Order.
package online

// Payload is the JSON shape published by `online/` into SQS, byte-for-byte
// identical to what the vendor POSTs. Field names match the snake_case keys
// the vendor uses; do NOT rename them without coordinating with online/ AND
// the vendor.
type Payload struct {
	TVer             string            `json:"tVer"`
	OrderID          int               `json:"order_id"`
	StoreID          int64             `json:"store_id"`
	VendorStoreID    string            `json:"vendor_store_id"`
	StoreName        string            `json:"store_name"`
	ServiceType      string            `json:"service_type"`
	SubmittedDate    string            `json:"submitted_date"`
	PrintDate        string            `json:"print_date"`
	DeferredDate     string            `json:"deferred_date,omitempty"`
	MiscCharges      []MiscCharge      `json:"misc_charges,omitempty"`
	Tip              float64           `json:"tip"`
	Taxes            []Tax             `json:"taxes,omitempty"`
	IsTaxExempt      bool              `json:"is_tax_exempt"`
	OrderTotal       float64           `json:"order_total"`
	BalanceOwing     float64           `json:"balance_owing"`
	Notes            string            `json:"notes,omitempty"`
	Customer         Customer          `json:"customer"`
	DeliveryAddress  *DeliveryAddress  `json:"delivery_address,omitempty"`
	DeliveryProvider *DeliveryProvider `json:"delivery_provider,omitempty"`
	Payments         []Payment         `json:"payments,omitempty"`
	Items            []Item            `json:"items,omitempty"`
	Coupons          []Coupon          `json:"coupons,omitempty"`
}

type MiscCharge struct {
	MiscChargeName   string  `json:"misc_charge_name"`
	MiscChargeDesc   string  `json:"misc_charge_desc"`
	MiscChargeAmount float64 `json:"misc_charge_amount"`
}

type Tax struct {
	TaxName   string  `json:"tax_name"`
	TaxAmount float64 `json:"tax_amount"`
}

type Customer struct {
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Company   string `json:"company"`
	Phone     string `json:"phone"`
	Ext       string `json:"ext"`
	Email     string `json:"email"`
}

type DeliveryAddress struct {
	Street       string `json:"street"`
	CrossStreets string `json:"cross_streets"`
	Suite        string `json:"suite"`
	Buz          string `json:"buz"`
	City         string `json:"city"`
	State        string `json:"state"`
	Zip          string `json:"zip"`
}

type DeliveryProvider struct {
	ProviderName string `json:"provider_name"`
	Status       string `json:"status"`
	DeliveryID   string `json:"delivery_id"`
	TrackingURL  string `json:"tracking_url"`
	PickupDate   string `json:"pickup_date"`
}

type Payment struct {
	Type          string  `json:"type"`
	Amount        float64 `json:"amount"`
	CardNumber    string  `json:"card_number"`
	CardHolder    string  `json:"card_holder"`
	AuthCode      string  `json:"auth_code"`
	TransactionID string  `json:"transaction_id"`
	Token         string  `json:"token"`
}

type Item struct {
	ItemID    int        `json:"item_id,omitempty"`
	Name      string     `json:"name"`
	SizeID    int        `json:"size_id"`
	SizeName  string     `json:"size_name"`
	Quantity  int        `json:"quantity"`
	Price     float64    `json:"price"`
	PLU       string     `json:"plu"`
	Who       string     `json:"who"`
	GroupID   string     `json:"group_id"`
	Notes     string     `json:"notes"`
	Modifiers []Modifier `json:"modifiers,omitempty"`
}

type Modifier struct {
	Side     string  `json:"side"`
	Name     string  `json:"name"`
	Quantity int     `json:"quantity"`
	PLU      string  `json:"plu"`
	Price    float64 `json:"price"`
	Action   string  `json:"action"`
}

type Coupon struct {
	Serial  string  `json:"serial"`
	PLU     string  `json:"plu"`
	Name    string  `json:"name"`
	Value   float64 `json:"value"`
	GroupID string  `json:"group_id"`
}
