-- +goose Up
-- +goose StatementBegin
-- weborder stage 2: the storefront. One platform key (the storefront
-- server's, qpk_…) plus the host it serves picks a store: site_domains maps
-- each host to its location, and the location's built-in web channel takes
-- the request. Platform subdomains (<slug>.quiccpos.com) are added by an
-- admin and verified at once; a store's own domain starts unverified and
-- never resolves until it is verified. site_settings holds the Website
-- page's look and texts (draft + published), versioned for the storefront.
CREATE TABLE platform_keys (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text        NOT NULL,
    key_hash     text        NOT NULL UNIQUE,
    key_prefix   text        NOT NULL,
    revoked_at   timestamptz,
    last_used_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE site_domains (
    host        text        PRIMARY KEY,
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    is_primary  boolean     NOT NULL DEFAULT false,
    verified    boolean     NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_site_domains_location ON site_domains (location_id);
CREATE UNIQUE INDEX idx_site_domains_primary ON site_domains (location_id) WHERE is_primary;

CREATE TABLE site_settings (
    location_id uuid        PRIMARY KEY,
    tenant_id   uuid        NOT NULL,
    draft       jsonb       NOT NULL DEFAULT '{}'::jsonb,
    published   jsonb       NOT NULL DEFAULT '{}'::jsonb,
    version     bigint      NOT NULL DEFAULT 1,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS site_settings;
DROP TABLE IF EXISTS site_domains;
DROP TABLE IF EXISTS platform_keys;
-- +goose StatementEnd
