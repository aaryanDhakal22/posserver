-- +goose Up
-- +goose StatementBegin
-- main 3.0 step 3: store status. One ordering row per location (created
-- with these defaults on first use): which order types are on, the ready-time
-- estimates channels show, how many days ahead orders may be scheduled
-- (0 = ASAP only), the pause (paused_at set; paused_until NULL = until
-- someone unpauses; a note for customers) and when 86'd items were last
-- brought back (restocked_at, at the location's daily_reset_hour).
-- store_hours_exceptions: dates that are closed or keep their own hours
-- (hours = [{open, close}] like opening_hours; past midnight counts on the
-- day it started).
CREATE TABLE store_ordering (
    location_id          uuid        PRIMARY KEY,
    tenant_id            uuid        NOT NULL,
    pickup_enabled       boolean     NOT NULL DEFAULT true,
    delivery_enabled     boolean     NOT NULL DEFAULT true,
    pickup_eta_minutes   integer     NOT NULL DEFAULT 20 CHECK (pickup_eta_minutes BETWEEN 0 AND 240),
    delivery_eta_minutes integer     NOT NULL DEFAULT 45 CHECK (delivery_eta_minutes BETWEEN 0 AND 240),
    schedule_days        integer     NOT NULL DEFAULT 7 CHECK (schedule_days BETWEEN 0 AND 30),
    paused_at            timestamptz,
    paused_until         timestamptz,
    pause_note           text        NOT NULL DEFAULT '',
    restocked_at         timestamptz,
    updated_at           timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);

CREATE TABLE store_hours_exceptions (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    day         date        NOT NULL,
    closed      boolean     NOT NULL,
    hours       jsonb       NOT NULL DEFAULT '[]'::jsonb,
    note        text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (location_id, day),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS store_hours_exceptions;
DROP TABLE IF EXISTS store_ordering;
-- +goose StatementEnd
