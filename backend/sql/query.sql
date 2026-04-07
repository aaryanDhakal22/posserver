-- name: CreateItem :copyfrom
INSERT INTO items(Name, SizeID, SizeName, Quantity, Price, PLU, Who, GroupID, Notes, OrderID)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: CreateCoupon :copyfrom
INSERT INTO coupons(Serial, PLU, Name, Value, GroupID, OrderID)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: CreateMiscCharge :copyfrom
INSERT INTO misc_charges(MiscChargeName, MiscChargeDesc, MiscChargeAmount, OrderID)
VALUES ($1, $2, $3, $4);

-- name: CreateTax :copyfrom
INSERT INTO taxes(TaxName, TaxAmount, OrderID)
VALUES ($1, $2, $3);

-- name: CreateDeliveryProvider :one
INSERT INTO delivery_providers(ProviderName, Status, DeliveryID, TrackingURL, PickupDate)
VALUES ($1, $2, $3, $4, $5)
RETURNING id;

-- name: CreateDeliveryAddress :one
INSERT INTO delivery_addresses(Street, CrossStreets, Suite, Buz, City, State, Zip)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id;

-- name: CreatePayment :copyfrom
INSERT INTO payments(PaymentType, Amount, CardNumber, CardHolder, AuthCode, TransactionID, Token, OrderID)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: CreateModifier :copyfrom
INSERT INTO modifiers(Side, Name, Quantity, PLU, Price, Action, ItemID)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: CreateCustomer :one
INSERT INTO customers(FirstName, LastName, Company, Phone, Ext, Email)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id;

-- name: CreateOrder :one
INSERT INTO orders(TVer, OrderID, StoreID, VendorStoreID, StoreName, ServiceType, SubmittedDate, PrintDate, DeferredDate, IsTaxExempt, OrderTotal, BalanceOwing, Notes, Tip, Customer, DeliveryAddress, DeliveryProvider)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
RETURNING OrderID;

-- name: GetAllOrders :many
SELECT * FROM orders;

-- name: GetOrderByID :one
SELECT * FROM orders WHERE OrderID = $1;

-- name: GetLatest :one
Select * FROM orders ORDER BY OrderID DESC LIMIT 1;

-- name: GetOrdersPage :many
SELECT * FROM orders ORDER BY OrderID DESC LIMIT $1 OFFSET $2;

-- name: CreateSingleItem :one
INSERT INTO items(Name, SizeID, SizeName, Quantity, Price, PLU, Who, GroupID, Notes, OrderID)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id;

-- name: GetItemsByOrderID :many
SELECT id, name, sizeid, sizename, quantity, price, plu, who, groupid, notes, orderid FROM items WHERE OrderID = $1;

-- name: GetModifiersByItemID :many
SELECT id, side, name, quantity, plu, price, action, itemid FROM modifiers WHERE ItemID = $1;

-- name: GetCouponsByOrderID :many
SELECT id, serial, plu, name, value, groupid, orderid FROM coupons WHERE OrderID = $1;

-- name: GetPaymentsByOrderID :many
SELECT id, paymenttype, amount, cardnumber, cardholder, authcode, transactionid, token, orderid FROM payments WHERE OrderID = $1;

-- name: GetTaxesByOrderID :many
SELECT id, taxname, taxamount, orderid FROM taxes WHERE OrderID = $1;

-- name: GetMiscChargesByOrderID :many
SELECT id, miscchargename, miscchargedesc, miscchargeamount, orderid FROM misc_charges WHERE OrderID = $1;

-- name: GetCustomerByID :one
SELECT id, firstname, lastname, company, phone, ext, email FROM customers WHERE id = $1;

-- name: GetDeliveryAddressByID :one
SELECT id, street, crossstreets, suite, buz, city, state, zip FROM delivery_addresses WHERE id = $1;

-- name: GetDeliveryProviderByID :one
SELECT id, providername, status, deliveryid, trackingurl, pickupdate FROM delivery_providers WHERE id = $1;
