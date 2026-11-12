-- +goose Up
-- +goose StatementBegin
-- TERMINAL-PLAN Stage 0d: the counter terminal's staff, the store's terminal
-- settings and the staff journal the Pi syncs up.
--
-- staff: the people who log in to a terminal with a 2-4 digit PIN. Only a
-- bcrypt hash is kept (the Pi gets the hashes so login works offline); a PIN
-- is unique among a store's active staff, checked against the hashes.
-- limits: the person's own overrides of the role defaults
-- {refund_cents?, discount_pct?}.
CREATE TABLE staff (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    first_name  text        NOT NULL,
    last_name   text        NOT NULL DEFAULT '',
    pin_hash    text        NOT NULL,
    role        text        NOT NULL CHECK (role IN ('cashier', 'manager', 'driver')),
    limits      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    active      boolean     NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_staff_location ON staff (location_id);

-- terminal_settings: one row per store (absent = the defaults); version is
-- bumped by every staff or settings write (GET /store/v1/agent/terminal-config).
CREATE TABLE terminal_settings (
    location_id uuid        PRIMARY KEY,
    tenant_id   uuid        NOT NULL,
    settings    jsonb       NOT NULL DEFAULT '{}'::jsonb,
    version     bigint      NOT NULL DEFAULT 0,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);

-- staff_journal: every terminal action, as the Pi recorded it (floor event
-- journal.entries; idempotent by the Pi's entry id). staff_id / order_id are
-- not foreign keys: the journal outlives deleted staff and orders.
CREATE TABLE staff_journal (
    id          uuid        PRIMARY KEY,
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    at          timestamptz NOT NULL,
    staff_id    uuid,
    device_id   text        NOT NULL DEFAULT '',
    action      text        NOT NULL,
    order_id    uuid,
    detail      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    received_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_staff_journal_location_at ON staff_journal (location_id, at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE staff_journal;
DROP TABLE terminal_settings;
DROP TABLE staff;
-- +goose StatementEnd
