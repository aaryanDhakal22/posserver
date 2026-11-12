-- +goose Up
-- +goose StatementBegin

-- Floor flow read model (owner decisions, 2026-10-15). The restaurant's agent
-- decides an order's floor state (accept + kitchen number + prep estimate,
-- ready, on delivery, delivered) and pushes each change here as a floor event;
-- main applies them forward-only and serves them (later: customer tracking).

-- Mirrored floor fields. kitchen_order_number already existed (reserved).
ALTER TABLE orders ADD COLUMN estimated_ready_at timestamptz;
ALTER TABLE orders ADD COLUMN driver_name        text NOT NULL DEFAULT '';

-- Idempotency: every agent floor event id seen, so a retried POST is a no-op.
CREATE TABLE floor_events_seen (
    event_id    uuid        PRIMARY KEY,
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    order_id    uuid,
    type        text        NOT NULL,
    result      text        NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_floor_events_seen_location ON floor_events_seen (location_id, received_at DESC);

-- The location's delivery-driver roster as the agent last reported it
-- (the agent owns it; main mirrors it whole on every drivers.roster event).
CREATE TABLE drivers (
    id          uuid        PRIMARY KEY,
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    name        text        NOT NULL,
    position    integer     NOT NULL,
    status      text        NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_drivers_location ON drivers (location_id, position);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS drivers;
DROP TABLE IF EXISTS floor_events_seen;
ALTER TABLE orders DROP COLUMN IF EXISTS driver_name;
ALTER TABLE orders DROP COLUMN IF EXISTS estimated_ready_at;
-- +goose StatementEnd
