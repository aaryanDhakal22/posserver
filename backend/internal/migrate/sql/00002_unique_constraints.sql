-- +goose Up

ALTER TABLE customers ADD CONSTRAINT customers_phone_key UNIQUE (Phone);
ALTER TABLE delivery_providers ADD CONSTRAINT delivery_providers_deliveryid_key UNIQUE (DeliveryID);

-- +goose Down

ALTER TABLE customers DROP CONSTRAINT IF EXISTS customers_phone_key;
ALTER TABLE delivery_providers DROP CONSTRAINT IF EXISTS delivery_providers_deliveryid_key;
