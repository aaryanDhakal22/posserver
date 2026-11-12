-- +goose Up
-- +goose StatementBegin
-- TERMINAL-PLAN Stage 0c: customers for the counter. First + last name next
-- to the website's full `name` (main keeps them in step: writing first/last
-- sets name = "first last"; the website writing name splits it, the last word
-- being the last name), the store's notes, where the account came from, and
-- who last wrote the name / notes (the bulk import never overwrites a name or
-- notes the website or the counter wrote).
ALTER TABLE customers
    ADD COLUMN first_name   text NOT NULL DEFAULT '',
    ADD COLUMN last_name    text NOT NULL DEFAULT '',
    ADD COLUMN notes        text NOT NULL DEFAULT '',
    ADD COLUMN source       text NOT NULL DEFAULT 'web' CHECK (source IN ('web', 'counter', 'import')),
    ADD COLUMN name_source  text NOT NULL DEFAULT '' CHECK (name_source IN ('', 'web', 'counter', 'import')),
    ADD COLUMN notes_source text NOT NULL DEFAULT '' CHECK (notes_source IN ('', 'web', 'counter', 'import'));

-- Every account so far came from the website.
UPDATE customers SET
    first_name  = CASE WHEN btrim(name) ~ '\s' THEN regexp_replace(btrim(name), '\s+\S+$', '') ELSE btrim(name) END,
    last_name   = CASE WHEN btrim(name) ~ '\s' THEN substring(btrim(name) FROM '(\S+)$') ELSE '' END,
    name_source = CASE WHEN name <> '' THEN 'web' ELSE '' END;

-- The Pi's change feed pages by (updated_at, id).
CREATE INDEX idx_customers_changes ON customers (location_id, updated_at, id);

-- A deleted customer leaves a tombstone for the feed (pruned after 90 days;
-- a Pi further behind starts its copy over).
CREATE TABLE customer_tombstones (
    customer_id uuid        PRIMARY KEY,
    location_id uuid        NOT NULL,
    deleted_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_customer_tombstones_changes ON customer_tombstones (location_id, deleted_at, customer_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION customers_tombstone() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO customer_tombstones (customer_id, location_id) VALUES (OLD.id, OLD.location_id)
    ON CONFLICT (customer_id) DO UPDATE SET deleted_at = now();
    RETURN OLD;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER customers_tombstone AFTER DELETE ON customers
    FOR EACH ROW EXECUTE FUNCTION customers_tombstone();
-- +goose StatementEnd

-- +goose StatementBegin
-- Any change the Pi keeps (names, phone, notes, addresses) moves the
-- customer's updated_at, whoever writes it; a sign-in alone does not.
CREATE FUNCTION customers_touch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.phone, NEW.name, NEW.first_name, NEW.last_name, NEW.notes, NEW.source)
       IS DISTINCT FROM (OLD.phone, OLD.name, OLD.first_name, OLD.last_name, OLD.notes, OLD.source) THEN
        NEW.updated_at = now();
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER customers_touch BEFORE UPDATE ON customers
    FOR EACH ROW EXECUTE FUNCTION customers_touch();
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION customer_addresses_touch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE customers SET updated_at = now() WHERE id = COALESCE(NEW.customer_id, OLD.customer_id);
    RETURN NULL;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER customer_addresses_touch AFTER INSERT OR UPDATE OR DELETE ON customer_addresses
    FOR EACH ROW EXECUTE FUNCTION customer_addresses_touch();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS customer_addresses_touch ON customer_addresses;
DROP FUNCTION IF EXISTS customer_addresses_touch();
DROP TRIGGER IF EXISTS customers_touch ON customers;
DROP FUNCTION IF EXISTS customers_touch();
DROP TRIGGER IF EXISTS customers_tombstone ON customers;
DROP FUNCTION IF EXISTS customers_tombstone();
DROP TABLE IF EXISTS customer_tombstones;
DROP INDEX IF EXISTS idx_customers_changes;
ALTER TABLE customers DROP COLUMN notes_source, DROP COLUMN name_source, DROP COLUMN source,
    DROP COLUMN notes, DROP COLUMN last_name, DROP COLUMN first_name;
-- +goose StatementEnd
