-- +goose Up
-- The card auth code the vendor sends was only in the first order frame; keep
-- it so a reprint, a redelivered order or a restored Pi still prints it.
ALTER TABLE payments ADD COLUMN auth_code text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE payments DROP COLUMN auth_code;
