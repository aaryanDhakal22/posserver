-- +goose Up
-- +goose StatementBegin
-- main 3.0 step 5: which channel an order came through. Written in the
-- order's own transaction; orders itself is untouched (its rows and the
-- Snapshot stay as deployed agents know them). client_order_id is the
-- channel's idempotency key (one order per key per channel); payment holds
-- the gateway + reference a refund needs. Brygid's orders link to the
-- location's built-in vendor channel with no client id.
CREATE TABLE channel_orders (
    order_id        uuid        PRIMARY KEY REFERENCES orders(id) ON DELETE CASCADE,
    channel_id      uuid        REFERENCES channels(id) ON DELETE SET NULL,
    tenant_id       uuid        NOT NULL,
    location_id     uuid        NOT NULL,
    client_order_id text        NOT NULL DEFAULT '',
    payment         jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_channel_orders_client ON channel_orders (channel_id, client_order_id) WHERE client_order_id <> '';
CREATE INDEX idx_channel_orders_channel ON channel_orders (channel_id, created_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS channel_orders;
-- +goose StatementEnd
