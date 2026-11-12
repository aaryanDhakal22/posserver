-- +goose Up
-- +goose StatementBegin

-- store_id: the external vendor's numeric store id (e.g. 3282 = Lombardi's).
-- The online caster resolves an order's tenant by the vendor body's store_id
-- first, falling back to the fuzzy store_name match when it's absent/unmapped.
-- Optional (tenants not on the vendor have none); unique when present.
ALTER TABLE tenants ADD COLUMN store_id bigint;
CREATE UNIQUE INDEX idx_tenants_store_id ON tenants (store_id) WHERE store_id IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_tenants_store_id;
ALTER TABLE tenants DROP COLUMN IF EXISTS store_id;
-- +goose StatementEnd
