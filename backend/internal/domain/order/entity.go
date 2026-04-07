package order

// Root request payload for submitting an order to Brygid
type Order struct {
	TVer          string
	OrderID       int
	StoreID       int64
	VendorStoreID string
	StoreName     string
	ServiceType   string
	SubmittedDate string
	PrintDate     string
	DeferredDate  string

	// Additional charges applied to the order such as delivery or service fees
	MiscCharges []MiscCharge

	Tip float64

	// Taxes applied to the order unless it is tax exempt
	Taxes []Tax

	IsTaxExempt  bool
	OrderTotal   float64
	BalanceOwing float64
	Notes        string

	// Customer personal and contact information
	Customer Customer

	// Delivery address details (only valid for delivery orders)
	DeliveryAddress *DeliveryAddress

	// Third-party delivery provider details such as DoorDash or Postmates
	DeliveryProvider *DeliveryProvider

	// Payments applied to the order
	Payments []Payment

	// Items included in the order
	Items []Item

	// Coupons applied to the order
	Coupons []Coupon
}

// Represents additional charges added to an order
type MiscCharge struct {
	MiscChargeName   string
	MiscChargeDesc   string
	MiscChargeAmount float64
}

// Represents a tax applied to the order
type Tax struct {
	TaxName   string
	TaxAmount float64
}

// Holds customer identity and contact details
type Customer struct {
	FirstName string
	LastName  string
	Company   string
	Phone     string
	Ext       string
	Email     string
}

// Contains delivery location details for delivery orders
type DeliveryAddress struct {
	Street       string
	CrossStreets string
	Suite        string
	Buz          string
	City         string
	State        string
	Zip          string
}

// Describes the third-party delivery service handling the order
type DeliveryProvider struct {
	ProviderName string
	Status       string
	DeliveryID   string
	TrackingURL  string
	PickupDate   string
}

// Represents a payment made toward the order total
type Payment struct {
	Type          string
	Amount        float64
	CardNumber    string
	CardHolder    string
	AuthCode      string
	TransactionID string
	Token         string
}

// Represents a purchasable item within an order
type Item struct {
	ItemID    int
	Name      string
	SizeID    int
	SizeName  string
	Quantity  int
	Price     float64
	PLU       string
	Who       string
	GroupID   string
	Notes     string
	Modifiers []Modifier
}

// Represents a modifier attached to an item
type Modifier struct {
	Side     string
	Name     string
	Quantity int
	PLU      string
	Price    float64
	Action   string
}

// Represents a discount or promotional coupon applied to the order
type Coupon struct {
	Serial  string
	PLU     string
	Name    string
	Value   float64
	GroupID string
}

type CreateOrderResult struct {
	Status          string
	ExtOrderID      string
	OrderPlacedTime string
}

// CreateOrderCommand is the envelope received from the SQS queue.
// Payload is a JSON-encoded Order.
type CreateOrderCommand struct {
	OrderID     string
	Payload     string
	DateCreated string
	CreatedAt   string
}
