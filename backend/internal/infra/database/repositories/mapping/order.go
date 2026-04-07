package mapper

import (
	"strconv"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/models"

	"github.com/jackc/pgx/v5/pgtype"
)

// pgtype helpers

func StrToPg(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: true}
}

func IntToPg(i int) pgtype.Int4 {
	return pgtype.Int4{Int32: int32(i), Valid: true}
}

func Int32ToPg(i int32) pgtype.Int4 {
	return pgtype.Int4{Int32: i, Valid: true}
}

func FlToPg(f float64) pgtype.Float4 {
	return pgtype.Float4{Float32: float32(f), Valid: true}
}

func BoolToPg(b bool) pgtype.Bool {
	return pgtype.Bool{Bool: b, Valid: true}
}

// domain → model param mappers

func ToModifierParams(itemID int32, modifiers []order.Modifier) []models.CreateModifierParams {
	params := make([]models.CreateModifierParams, 0, len(modifiers))
	for _, m := range modifiers {
		params = append(params, models.CreateModifierParams{
			Side:     StrToPg(m.Side),
			Name:     StrToPg(m.Name),
			Quantity: Int32ToPg(int32(m.Quantity)),
			Plu:      StrToPg(m.PLU),
			Price:    FlToPg(m.Price),
			Action:   StrToPg(m.Action),
			Itemid:   Int32ToPg(itemID),
		})
	}
	return params
}

func ToCouponParams(orderID int, coupons []order.Coupon) []models.CreateCouponParams {
	params := make([]models.CreateCouponParams, 0, len(coupons))
	for _, c := range coupons {
		params = append(params, models.CreateCouponParams{
			Serial:  StrToPg(c.Serial),
			Plu:     StrToPg(c.PLU),
			Name:    StrToPg(c.Name),
			Value:   FlToPg(c.Value),
			Groupid: StrToPg(c.GroupID),
			Orderid: IntToPg(orderID),
		})
	}
	return params
}

func ToPaymentParams(orderID int, payments []order.Payment) []models.CreatePaymentParams {
	params := make([]models.CreatePaymentParams, 0, len(payments))
	for _, p := range payments {
		params = append(params, models.CreatePaymentParams{
			Paymenttype:   StrToPg(p.Type),
			Amount:        FlToPg(p.Amount),
			Cardnumber:    StrToPg(p.CardNumber),
			Cardholder:    StrToPg(p.CardHolder),
			Authcode:      StrToPg(p.AuthCode),
			Transactionid: StrToPg(p.TransactionID),
			Token:         StrToPg(p.Token),
			Orderid:       IntToPg(orderID),
		})
	}
	return params
}

func ToTaxParams(orderID int, taxes []order.Tax) []models.CreateTaxParams {
	params := make([]models.CreateTaxParams, 0, len(taxes))
	for _, t := range taxes {
		params = append(params, models.CreateTaxParams{
			Taxname:   StrToPg(t.TaxName),
			Taxamount: FlToPg(t.TaxAmount),
			Orderid:   IntToPg(orderID),
		})
	}
	return params
}

func ToMiscChargeParams(orderID int, charges []order.MiscCharge) []models.CreateMiscChargeParams {
	params := make([]models.CreateMiscChargeParams, 0, len(charges))
	for _, mc := range charges {
		params = append(params, models.CreateMiscChargeParams{
			Miscchargename:   StrToPg(mc.MiscChargeName),
			Miscchargedesc:   StrToPg(mc.MiscChargeDesc),
			Miscchargeamount: FlToPg(mc.MiscChargeAmount),
			Orderid:          IntToPg(orderID),
		})
	}
	return params
}

func ToCreateOrderParams(o *order.Order, customerID, addrID, providerID int32) models.CreateOrderParams {
	tver, _ := strconv.Atoi(o.TVer)
	var addrPg, providerPg pgtype.Int4
	if addrID != 0 {
		addrPg = Int32ToPg(addrID)
	}
	if providerID != 0 {
		providerPg = Int32ToPg(providerID)
	}
	return models.CreateOrderParams{
		Tver:             Int32ToPg(int32(tver)),
		Orderid:          int32(o.OrderID),
		Storeid:          Int32ToPg(int32(o.StoreID)),
		Vendorstoreid:    StrToPg(o.VendorStoreID),
		Storename:        StrToPg(o.StoreName),
		Servicetype:      StrToPg(o.ServiceType),
		Submitteddate:    StrToPg(o.SubmittedDate),
		Printdate:        StrToPg(o.PrintDate),
		Deferreddate:     StrToPg(o.DeferredDate),
		Istaxexempt:      BoolToPg(o.IsTaxExempt),
		Ordertotal:       FlToPg(o.OrderTotal),
		Balanceowing:     FlToPg(o.BalanceOwing),
		Notes:            StrToPg(o.Notes),
		Tip:              FlToPg(o.Tip),
		Customer:         Int32ToPg(customerID),
		Deliveryaddress:  addrPg,
		Deliveryprovider: providerPg,
	}
}

func ToSingleItemParams(orderID int, item order.Item) models.CreateSingleItemParams {
	return models.CreateSingleItemParams{
		Name:     StrToPg(item.Name),
		Sizeid:   IntToPg(item.SizeID),
		Sizename: StrToPg(item.SizeName),
		Quantity: IntToPg(item.Quantity),
		Price:    FlToPg(item.Price),
		Plu:      StrToPg(item.PLU),
		Who:      StrToPg(item.Who),
		Groupid:  StrToPg(item.GroupID),
		Notes:    StrToPg(item.Notes),
		Orderid:  IntToPg(orderID),
	}
}

func ToCustomerParams(c order.Customer) models.CreateCustomerParams {
	return models.CreateCustomerParams{
		Firstname: StrToPg(c.FirstName),
		Lastname:  StrToPg(c.LastName),
		Company:   StrToPg(c.Company),
		Phone:     StrToPg(c.Phone),
		Ext:       StrToPg(c.Ext),
		Email:     StrToPg(c.Email),
	}
}

func ToDeliveryAddressParams(a order.DeliveryAddress) models.CreateDeliveryAddressParams {
	return models.CreateDeliveryAddressParams{
		Street:       StrToPg(a.Street),
		Crossstreets: StrToPg(a.CrossStreets),
		Suite:        StrToPg(a.Suite),
		Buz:          StrToPg(a.Buz),
		City:         StrToPg(a.City),
		State:        StrToPg(a.State),
		Zip:          StrToPg(a.Zip),
	}
}

func ToDeliveryProviderParams(p order.DeliveryProvider) models.CreateDeliveryProviderParams {
	return models.CreateDeliveryProviderParams{
		Providername: StrToPg(p.ProviderName),
		Status:       StrToPg(p.Status),
		Deliveryid:   StrToPg(p.DeliveryID),
		Trackingurl:  StrToPg(p.TrackingURL),
		Pickupdate:   StrToPg(p.PickupDate),
	}
}

// model → domain mapper

func ToOrderDomain(
	o models.Order,
	customer models.Customer,
	addr *models.DeliveryAddress,
	provider *models.DeliveryProvider,
	items []models.Item,
	modifiersByItem map[int32][]models.Modifier,
	coupons []models.Coupon,
	payments []models.Payment,
	taxes []models.Tax,
	miscCharges []models.MiscCharge,
) order.Order {
	domainItems := make([]order.Item, 0, len(items))
	for _, item := range items {
		mods := modifiersByItem[item.ID]
		domainMods := make([]order.Modifier, 0, len(mods))
		for _, m := range mods {
			domainMods = append(domainMods, order.Modifier{
				Side:     m.Side.String,
				Name:     m.Name.String,
				Quantity: int(m.Quantity.Int32),
				PLU:      m.Plu.String,
				Price:    float64(m.Price.Float32),
				Action:   m.Action.String,
			})
		}
		domainItems = append(domainItems, order.Item{
			ItemID:    int(item.ID),
			Name:      item.Name.String,
			SizeID:    int(item.Sizeid.Int32),
			SizeName:  item.Sizename.String,
			Quantity:  int(item.Quantity.Int32),
			Price:     float64(item.Price.Float32),
			PLU:       item.Plu.String,
			Who:       item.Who.String,
			GroupID:   item.Groupid.String,
			Notes:     item.Notes.String,
			Modifiers: domainMods,
		})
	}

	domainCoupons := make([]order.Coupon, 0, len(coupons))
	for _, c := range coupons {
		domainCoupons = append(domainCoupons, order.Coupon{
			Serial:  c.Serial.String,
			PLU:     c.Plu.String,
			Name:    c.Name.String,
			Value:   float64(c.Value.Float32),
			GroupID: c.Groupid.String,
		})
	}

	domainPayments := make([]order.Payment, 0, len(payments))
	for _, p := range payments {
		domainPayments = append(domainPayments, order.Payment{
			Type:          p.Paymenttype.String,
			Amount:        float64(p.Amount.Float32),
			CardNumber:    p.Cardnumber.String,
			CardHolder:    p.Cardholder.String,
			AuthCode:      p.Authcode.String,
			TransactionID: p.Transactionid.String,
			Token:         p.Token.String,
		})
	}

	domainTaxes := make([]order.Tax, 0, len(taxes))
	for _, t := range taxes {
		domainTaxes = append(domainTaxes, order.Tax{
			TaxName:   t.Taxname.String,
			TaxAmount: float64(t.Taxamount.Float32),
		})
	}

	domainMiscCharges := make([]order.MiscCharge, 0, len(miscCharges))
	for _, mc := range miscCharges {
		domainMiscCharges = append(domainMiscCharges, order.MiscCharge{
			MiscChargeName:   mc.Miscchargename.String,
			MiscChargeDesc:   mc.Miscchargedesc.String,
			MiscChargeAmount: float64(mc.Miscchargeamount.Float32),
		})
	}

	domainCustomer := order.Customer{
		FirstName: customer.Firstname.String,
		LastName:  customer.Lastname.String,
		Company:   customer.Company.String,
		Phone:     customer.Phone.String,
		Ext:       customer.Ext.String,
		Email:     customer.Email.String,
	}

	var domainAddr *order.DeliveryAddress
	if addr != nil {
		domainAddr = &order.DeliveryAddress{
			Street:       addr.Street.String,
			CrossStreets: addr.Crossstreets.String,
			Suite:        addr.Suite.String,
			Buz:          addr.Buz.String,
			City:         addr.City.String,
			State:        addr.State.String,
			Zip:          addr.Zip.String,
		}
	}

	var domainProvider *order.DeliveryProvider
	if provider != nil {
		domainProvider = &order.DeliveryProvider{
			ProviderName: provider.Providername.String,
			Status:       provider.Status.String,
			DeliveryID:   provider.Deliveryid.String,
			TrackingURL:  provider.Trackingurl.String,
			PickupDate:   provider.Pickupdate.String,
		}
	}

	return order.Order{
		TVer:             strconv.Itoa(int(o.Tver.Int32)),
		OrderID:          int(o.Orderid),
		StoreID:          int64(o.Storeid.Int32),
		VendorStoreID:    o.Vendorstoreid.String,
		StoreName:        o.Storename.String,
		ServiceType:      o.Servicetype.String,
		SubmittedDate:    o.Submitteddate.String,
		PrintDate:        o.Printdate.String,
		DeferredDate:     o.Deferreddate.String,
		IsTaxExempt:      o.Istaxexempt.Bool,
		OrderTotal:       float64(o.Ordertotal.Float32),
		BalanceOwing:     float64(o.Balanceowing.Float32),
		Notes:            o.Notes.String,
		Tip:              float64(o.Tip.Float32),
		Customer:         domainCustomer,
		DeliveryAddress:  domainAddr,
		DeliveryProvider: domainProvider,
		Items:            domainItems,
		Coupons:          domainCoupons,
		Payments:         domainPayments,
		Taxes:            domainTaxes,
		MiscCharges:      domainMiscCharges,
	}
}
