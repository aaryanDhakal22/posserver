-- +goose Up
-- +goose StatementBegin
-- Agent reliability + headless Pi recovery (2026-10-16).
--
-- agent_ack_at: the agent confirmed it stored the order. Orders still
--   unacked are re-sent over SSE until they are (no order is ever lost to a
--   dropped stream). Existing orders count as acked.
-- agent_status / agent_status_at / agent_batch_id: the agent's full floor
--   status for the order (from its order.status events) and which agent batch
--   it belongs to — what a replacement Pi restores from.
ALTER TABLE orders ADD COLUMN agent_ack_at    timestamptz;
ALTER TABLE orders ADD COLUMN agent_status    jsonb;
ALTER TABLE orders ADD COLUMN agent_status_at timestamptz;
ALTER TABLE orders ADD COLUMN agent_batch_id  bigint;
UPDATE orders SET agent_ack_at = now();
CREATE INDEX idx_orders_unacked ON orders (received_at) WHERE agent_ack_at IS NULL;
CREATE INDEX idx_orders_agent_batch ON orders (location_id, agent_batch_id);

-- The agent's latest floor state (open batch, kitchen counter, drivers +
-- queue + open shifts, settings), reported on every change.
CREATE TABLE agent_state (
    location_id uuid        PRIMARY KEY,
    tenant_id   uuid        NOT NULL,
    state       jsonb       NOT NULL,
    at          timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);

-- The agent's latest health report (disk, temperature, SD errors, uptime, …).
CREATE TABLE agent_health (
    location_id uuid        PRIMARY KEY,
    tenant_id   uuid        NOT NULL,
    device_id   uuid,
    health      jsonb       NOT NULL,
    at          timestamptz NOT NULL,
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);

-- Nightly copies of an agent's SQLite file (newest 2 per location kept). A
-- safety net; recovery restores from the live tables above.
CREATE TABLE agent_backups (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    device_id   uuid,
    size        bigint      NOT NULL,
    sha256      text        NOT NULL,
    data        bytea       NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_agent_backups_location ON agent_backups (location_id, created_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_backups;
DROP TABLE IF EXISTS agent_health;
DROP TABLE IF EXISTS agent_state;
DROP INDEX IF EXISTS idx_orders_agent_batch;
DROP INDEX IF EXISTS idx_orders_unacked;
ALTER TABLE orders DROP COLUMN IF EXISTS agent_batch_id;
ALTER TABLE orders DROP COLUMN IF EXISTS agent_status_at;
ALTER TABLE orders DROP COLUMN IF EXISTS agent_status;
ALTER TABLE orders DROP COLUMN IF EXISTS agent_ack_at;
-- +goose StatementEnd
