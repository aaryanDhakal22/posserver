-- +goose Up
-- +goose StatementBegin

-- One tenant = one restaurant = exactly one location (owner decision,
-- 2026-10-15). Manager login, the menu and online-order routing were already
-- pinned to a tenant's first location; this makes it structural.

-- 1. Refuse to guess: a tenant with several locations needs a human decision.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM locations GROUP BY tenant_id HAVING count(*) > 1) THEN
        RAISE EXCEPTION 'single-location migration: some tenants have more than one location — merge/delete the extras first';
    END IF;
END $$;

-- 2. Give every location-less tenant its location.
INSERT INTO locations (tenant_id, slug, name)
SELECT t.id, 'main', t.name
FROM tenants t
WHERE NOT EXISTS (SELECT 1 FROM locations l WHERE l.tenant_id = t.id);

-- 3. Enforce it. Supersedes the plain tenant_id index.
DROP INDEX IF EXISTS idx_locations_tenant;
CREATE UNIQUE INDEX idx_locations_one_per_tenant ON locations (tenant_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_locations_one_per_tenant;
CREATE INDEX IF NOT EXISTS idx_locations_tenant ON locations (tenant_id);
-- +goose StatementEnd
