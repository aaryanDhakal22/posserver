// Package dto contains the JSON-serialisable request/response shapes for the
// orders API. These are intentionally separate from the domain entities so
// that transport concerns (key naming, omitempty, versioning) never bleed into
// the core business model.
package dto

import "quiccpos/main/internal/domain/order"

// --------------------------------------------------------------------------
// Top-level order DTO
// --------------------------------------------------------------------------

type Order struct {
	TVer          string      `json:"tVer"`
	OrderID       int         `json:"order_id"`
	StoreID       int64       `json:"store_id"`
	VendorStoreID string      `json:"vendor_store_id"`
	StoreName     string      `json:"store_name"`
	ServiceType   string      `json:"service_type"`
	SubmittedDate string      `json:"submitted_date"`
	PrintDate     string      `json:"print_date"`
	DeferredDate  string      `json:"deferred_date,omitempty"`
	MiscCharges   []MiscCharge `json:"misc_charges,omitempty"`
	Tip           float64     `json:"tip"`
	Taxes         []Tax       `json:"taxes,omitempty"`
	IsTaxExempt   bool        `json:"is_tax_exempt"`
	OrderTotal    float64     `json:"order_total"`
	BalanceOwing  float64     `json:"balance_owing"`
	Notes         string      `json:"notes,omitempty"`
	Customer      Customer    `json:"customer"`
	DeliveryAddress  *DeliveryAddress  `json:"delivery_address,omitempty"`
	DeliveryProvider *DeliveryProvider `json:"delivery_provider,omitempty"`
	Payments      []Payment   `json:"payments,omitempty"`
	Items         []Item      `json:"items,omitempty"`
	Coupons       []Coupon    `json:"coupons,omitempty"`
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

// --------------------------------------------------------------------------
// DTO → domain
// --------------------------------------------------------------------------

func (d *Order) ToDomain() order.Order {
	o := order.Order{
		TVer:          d.TVer,
		OrderID:       d.OrderID,
		StoreID:       d.StoreID,
		VendorStoreID: d.VendorStoreID,
		StoreName:     d.StoreName,
		ServiceType:   d.ServiceType,
		SubmittedDate: d.SubmittedDate,
		PrintDate:     d.PrintDate,
		DeferredDate:  d.DeferredDate,
		Tip:           d.Tip,
		IsTaxExempt:   d.IsTaxExempt,
		OrderTotal:    d.OrderTotal,
		BalanceOwing:  d.BalanceOwing,
		Notes:         d.Notes,
		Customer: order.Customer{
			FirstName: d.Customer.FirstName,
			LastName:  d.Customer.LastName,
			Company:   d.Customer.Company,
			Phone:     d.Customer.Phone,
			Ext:       d.Customer.Ext,
			Email:     d.Customer.Email,
		},
	}

	for _, mc := range d.MiscCharges {
		o.MiscCharges = append(o.MiscCharges, order.MiscCharge{
			MiscChargeName:   mc.MiscChargeName,
			MiscChargeDesc:   mc.MiscChargeDesc,
			MiscChargeAmount: mc.MiscChargeAmount,
		})
	}
	for _, t := range d.Taxes {
		o.Taxes = append(o.Taxes, order.Tax{TaxName: t.TaxName, TaxAmount: t.TaxAmount})
	}
	for _, p := range d.Payments {
		o.Payments = append(o.Payments, order.Payment{
			Type:          p.Type,
			Amount:        p.Amount,
			CardNumber:    p.CardNumber,
			CardHolder:    p.CardHolder,
			AuthCode:      p.AuthCode,
			TransactionID: p.TransactionID,
			Token:         p.Token,
		})
	}
	for _, item := range d.Items {
		di := order.Item{
			ItemID:   item.ItemID,
			Name:     item.Name,
			SizeID:   item.SizeID,
			SizeName: item.SizeName,
			Quantity: item.Quantity,
			Price:    item.Price,
			PLU:      item.PLU,
			Who:      item.Who,
			GroupID:  item.GroupID,
			Notes:    item.Notes,
		}
		for _, m := range item.Modifiers {
			di.Modifiers = append(di.Modifiers, order.Modifier{
				Side:     m.Side,
				Name:     m.Name,
				Quantity: m.Quantity,
				PLU:      m.PLU,
				Price:    m.Price,
				Action:   m.Action,
			})
		}
		o.Items = append(o.Items, di)
	}
	for _, c := range d.Coupons {
		o.Coupons = append(o.Coupons, order.Coupon{
			Serial:  c.Serial,
			PLU:     c.PLU,
			Name:    c.Name,
			Value:   c.Value,
			GroupID: c.GroupID,
		})
	}

	if d.DeliveryAddress != nil {
		o.DeliveryAddress = &order.DeliveryAddress{
			Street:       d.DeliveryAddress.Street,
			CrossStreets: d.DeliveryAddress.CrossStreets,
			Suite:        d.DeliveryAddress.Suite,
			Buz:          d.DeliveryAddress.Buz,
			City:         d.DeliveryAddress.City,
			State:        d.DeliveryAddress.State,
			Zip:          d.DeliveryAddress.Zip,
		}
	}
	if d.DeliveryProvider != nil {
		o.DeliveryProvider = &order.DeliveryProvider{
			ProviderName: d.DeliveryProvider.ProviderName,
			Status:       d.DeliveryProvider.Status,
			DeliveryID:   d.DeliveryProvider.DeliveryID,
			TrackingURL:  d.DeliveryProvider.TrackingURL,
			PickupDate:   d.DeliveryProvider.PickupDate,
		}
	}

	return o
}

// --------------------------------------------------------------------------
// domain → DTO
// --------------------------------------------------------------------------

func FromDomain(o order.Order) Order {
	d := Order{
		TVer:          o.TVer,
		OrderID:       o.OrderID,
		StoreID:       o.StoreID,
		VendorStoreID: o.VendorStoreID,
		StoreName:     o.StoreName,
		ServiceType:   o.ServiceType,
		SubmittedDate: o.SubmittedDate,
		PrintDate:     o.PrintDate,
		DeferredDate:  o.DeferredDate,
		Tip:           o.Tip,
		IsTaxExempt:   o.IsTaxExempt,
		OrderTotal:    o.OrderTotal,
		BalanceOwing:  o.BalanceOwing,
		Notes:         o.Notes,
		Customer: Customer{
			FirstName: o.Customer.FirstName,
			LastName:  o.Customer.LastName,
			Company:   o.Customer.Company,
			Phone:     o.Customer.Phone,
			Ext:       o.Customer.Ext,
			Email:     o.Customer.Email,
		},
	}

	for _, mc := range o.MiscCharges {
		d.MiscCharges = append(d.MiscCharges, MiscCharge{
			MiscChargeName:   mc.MiscChargeName,
			MiscChargeDesc:   mc.MiscChargeDesc,
			MiscChargeAmount: mc.MiscChargeAmount,
		})
	}
	for _, t := range o.Taxes {
		d.Taxes = append(d.Taxes, Tax{TaxName: t.TaxName, TaxAmount: t.TaxAmount})
	}
	for _, p := range o.Payments {
		d.Payments = append(d.Payments, Payment{
			Type:          p.Type,
			Amount:        p.Amount,
			CardNumber:    p.CardNumber,
			CardHolder:    p.CardHolder,
			AuthCode:      p.AuthCode,
			TransactionID: p.TransactionID,
			Token:         p.Token,
		})
	}
	for _, item := range o.Items {
		di := Item{
			ItemID:   item.ItemID,
			Name:     item.Name,
			SizeID:   item.SizeID,
			SizeName: item.SizeName,
			Quantity: item.Quantity,
			Price:    item.Price,
			PLU:      item.PLU,
			Who:      item.Who,
			GroupID:  item.GroupID,
			Notes:    item.Notes,
		}
		for _, m := range item.Modifiers {
			di.Modifiers = append(di.Modifiers, Modifier{
				Side:     m.Side,
				Name:     m.Name,
				Quantity: m.Quantity,
				PLU:      m.PLU,
				Price:    m.Price,
				Action:   m.Action,
			})
		}
		d.Items = append(d.Items, di)
	}
	for _, c := range o.Coupons {
		d.Coupons = append(d.Coupons, Coupon{
			Serial:  c.Serial,
			PLU:     c.PLU,
			Name:    c.Name,
			Value:   c.Value,
			GroupID: c.GroupID,
		})
	}

	if o.DeliveryAddress != nil {
		d.DeliveryAddress = &DeliveryAddress{
			Street:       o.DeliveryAddress.Street,
			CrossStreets: o.DeliveryAddress.CrossStreets,
			Suite:        o.DeliveryAddress.Suite,
			Buz:          o.DeliveryAddress.Buz,
			City:         o.DeliveryAddress.City,
			State:        o.DeliveryAddress.State,
			Zip:          o.DeliveryAddress.Zip,
		}
	}
	if o.DeliveryProvider != nil {
		d.DeliveryProvider = &DeliveryProvider{
			ProviderName: o.DeliveryProvider.ProviderName,
			Status:       o.DeliveryProvider.Status,
			DeliveryID:   o.DeliveryProvider.DeliveryID,
			TrackingURL:  o.DeliveryProvider.TrackingURL,
			PickupDate:   o.DeliveryProvider.PickupDate,
		}
	}

	return d
}
