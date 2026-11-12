-- +goose Up
-- +goose StatementBegin
-- Cash drawers + the Z report (TERMINAL-PLAN "Drawers", "End of day"; floor
-- events drawer.opened / drawer.movement / drawer.closed / z.report from the
-- Pi, contract/main.md §2.5zb). Ids are the Pi's. A drawer session belongs to
-- one terminal (device) and one cashier; a closed session is never reopened
-- by a replayed drawer.opened. Movements carry no FK to their session: one
-- may arrive before its session row. One Z report per store per Pi batch (a
-- rebuilt Z replaces it); business_date = the store-local date the batch
-- opened, as the Pi sends it.
CREATE TABLE drawer_sessions (
    id                 uuid        PRIMARY KEY,
    tenant_id          uuid        NOT NULL,
    location_id        uuid        NOT NULL,
    device_id          text        NOT NULL DEFAULT '',
    device_name        text        NOT NULL DEFAULT '',
    staff_id           text        NOT NULL DEFAULT '',
    staff_name         text        NOT NULL DEFAULT '',
    batch_id           bigint,
    opened_at          timestamptz NOT NULL,
    start_cents        bigint      NOT NULL,
    handover_from      uuid,
    closed_at          timestamptz,
    closed_by          text        NOT NULL DEFAULT '',
    close_reason       text        NOT NULL DEFAULT '',
    counted_cents      bigint,
    denominations      jsonb,
    expected_cents     bigint,
    over_short_cents   bigint,
    flagged            boolean     NOT NULL DEFAULT false,
    cash_sales_cents   bigint      NOT NULL DEFAULT 0,
    cash_refunds_cents bigint      NOT NULL DEFAULT 0,
    cash_tips_cents    bigint      NOT NULL DEFAULT 0,
    pay_ins_cents      bigint      NOT NULL DEFAULT 0,
    pay_outs_cents     bigint      NOT NULL DEFAULT 0,
    no_sales           integer     NOT NULL DEFAULT 0,
    payments           integer     NOT NULL DEFAULT 0,
    updated_at         timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_drawer_sessions_location ON drawer_sessions (location_id, opened_at DESC);

CREATE TABLE drawer_movements (
    id           uuid        PRIMARY KEY,
    tenant_id    uuid        NOT NULL,
    location_id  uuid        NOT NULL,
    drawer_id    uuid        NOT NULL,
    kind         text        NOT NULL CHECK (kind IN ('pay_in', 'pay_out')),
    amount_cents bigint      NOT NULL CHECK (amount_cents > 0),
    reason       text        NOT NULL DEFAULT '',
    staff_id     text        NOT NULL DEFAULT '',
    approved_by  text        NOT NULL DEFAULT '',
    at           timestamptz NOT NULL,
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_drawer_movements_drawer ON drawer_movements (drawer_id, at);

CREATE TABLE z_reports (
    id            uuid        NOT NULL UNIQUE,
    tenant_id     uuid        NOT NULL,
    location_id   uuid        NOT NULL,
    batch_id      bigint      NOT NULL,
    business_date date        NOT NULL,
    opened_at     timestamptz,
    closed_at     timestamptz,
    report        jsonb       NOT NULL,
    received_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (location_id, batch_id),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_z_reports_date ON z_reports (location_id, business_date, closed_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS z_reports;
DROP TABLE IF EXISTS drawer_movements;
DROP TABLE IF EXISTS drawer_sessions;
-- +goose StatementEnd
