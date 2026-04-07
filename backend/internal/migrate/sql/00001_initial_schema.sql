-- +goose Up

CREATE TABLE IF NOT EXISTS customers (
    id         SERIAL PRIMARY KEY,
    FirstName  text,
    LastName   text,
    Company    text,
    Phone      text,
    Ext        text,
    Email      text
);

CREATE TABLE IF NOT EXISTS delivery_addresses (
    id           SERIAL PRIMARY KEY,
    Street       text,
    CrossStreets text,
    Suite        text,
    Buz          text,
    City         text,
    State        text,
    Zip          text
);

CREATE TABLE IF NOT EXISTS delivery_providers (
    id           SERIAL PRIMARY KEY,
    ProviderName text,
    Status       text,
    DeliveryID   text,
    TrackingURL  text,
    PickupDate   text
);

CREATE TABLE IF NOT EXISTS orders (
    TVer             integer,
    OrderID          integer PRIMARY KEY,
    StoreID          integer,
    VendorStoreID    text,
    StoreName        text,
    ServiceType      text,
    SubmittedDate    text,
    PrintDate        text,
    DeferredDate     text,
    IsTaxExempt      boolean,
    OrderTotal       real,
    BalanceOwing     real,
    Notes            text,
    Tip              real,
    Customer         integer REFERENCES customers(id),
    DeliveryAddress  integer REFERENCES delivery_addresses(id),
    DeliveryProvider integer REFERENCES delivery_providers(id)
);

CREATE TABLE IF NOT EXISTS payments (
    id            SERIAL PRIMARY KEY,
    PaymentType   text,
    Amount        real,
    CardNumber    text,
    CardHolder    text,
    AuthCode      text,
    TransactionID text,
    Token         text,
    OrderID       integer REFERENCES orders(OrderID)
);

CREATE TABLE IF NOT EXISTS items (
    id       SERIAL PRIMARY KEY,
    Name     text,
    SizeID   integer,
    SizeName text,
    Quantity integer,
    Price    real,
    PLU      text,
    Who      text,
    GroupID  text,
    Notes    text,
    OrderID  integer REFERENCES orders(OrderID)
);

CREATE TABLE IF NOT EXISTS modifiers (
    id       SERIAL PRIMARY KEY,
    Side     text,
    Name     text,
    Quantity integer,
    PLU      text,
    Price    real,
    Action   text,
    ItemID   integer REFERENCES items(id)
);

CREATE TABLE IF NOT EXISTS coupons (
    id      SERIAL PRIMARY KEY,
    Serial  text,
    PLU     text,
    Name    text,
    Value   real,
    GroupID text,
    OrderID integer REFERENCES orders(OrderID)
);

CREATE TABLE IF NOT EXISTS misc_charges (
    id               SERIAL PRIMARY KEY,
    MiscChargeName   text,
    MiscChargeDesc   text,
    MiscChargeAmount real,
    OrderID          integer REFERENCES orders(OrderID)
);

CREATE TABLE IF NOT EXISTS taxes (
    id        SERIAL PRIMARY KEY,
    TaxName   text,
    TaxAmount real,
    OrderID   integer REFERENCES orders(OrderID)
);

-- +goose Down

DROP TABLE IF EXISTS taxes;
DROP TABLE IF EXISTS misc_charges;
DROP TABLE IF EXISTS coupons;
DROP TABLE IF EXISTS modifiers;
DROP TABLE IF EXISTS items;
DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS delivery_providers;
DROP TABLE IF EXISTS delivery_addresses;
DROP TABLE IF EXISTS customers;
