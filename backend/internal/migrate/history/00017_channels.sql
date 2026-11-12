-- +goose Up
-- +goose StatementBegin
-- main 3.0: sales channels — every way an order can reach a store (the web
-- storefront, the call center, a counter POS terminal, a vendor feed like
-- Brygid) is a channel of one location, talking to main's /channel/v1 ports.
--
-- kind:        web | operator | counter | vendor
-- key_hash:    SHA-256 of the channel's bearer key (qch_…); NULL for a
--              built-in channel that never authenticates (the SQS vendor feed)
-- permissions: what the key may do ({menu, quote, orders, track, payments})
-- builtin:     created by main itself (the location's vendor feed), not by
--              an admin; can't be revoked or deleted
CREATE TABLE channels (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid        NOT NULL,
    location_id  uuid        NOT NULL,
    kind         text        NOT NULL CHECK (kind IN ('web', 'operator', 'counter', 'vendor')),
    name         text        NOT NULL,
    key_hash     text        UNIQUE,
    key_prefix   text        NOT NULL DEFAULT '',
    permissions  jsonb       NOT NULL DEFAULT '{}'::jsonb,
    builtin      boolean     NOT NULL DEFAULT false,
    revoked_at   timestamptz,
    last_used_at timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_channels_location ON channels (location_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS channels;
-- +goose StatementEnd
