-- +goose Up
-- +goose StatementBegin
-- SoftTouch is gone (housekeeping H3, 2026-11-11): drop the external stops.
--
-- geo_external_stops / geo_external_work held the POS deliveries the store hub
-- pulled from SoftTouch into the Pi (RPC geo.locate / geo.forget, main 2.1.0).
-- Nothing creates them any more: main answers geo.locate / geo.forget as an
-- unknown RPC (202, no-op). Their rows were points and short-lived address
-- text (24 h / 6 h); this drop is irreversible for whatever is left in them.
DROP TABLE IF EXISTS geo_external_work;
DROP TABLE IF EXISTS geo_external_stops;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- The tables as 00014 + 00015 + 00016 left them (empty).
CREATE TABLE IF NOT EXISTS geo_external_stops (
    order_id     uuid             PRIMARY KEY,
    tenant_id    uuid             NOT NULL,
    location_id  uuid             NOT NULL,
    state        text             NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'located', 'check', 'not_found')),
    lat          double precision,
    lng          double precision,
    quality      text             NOT NULL DEFAULT '',
    distance_m   integer          NOT NULL DEFAULT 0,
    duration_s   integer          NOT NULL DEFAULT 0,
    polyline     text             NOT NULL DEFAULT '',
    outside_area boolean          NOT NULL DEFAULT false,
    store_to_s   integer,
    to_store_s   integer,
    attempts     integer          NOT NULL DEFAULT 0,
    next_try_at  timestamptz      NOT NULL DEFAULT now(),
    last_error   text             NOT NULL DEFAULT '',
    pushed_at    timestamptz,
    created_at   timestamptz      NOT NULL DEFAULT now(),
    updated_at   timestamptz      NOT NULL DEFAULT now(),
    expires_at   timestamptz      NOT NULL,
    confirmed    boolean          NOT NULL DEFAULT false,
    corrected    boolean          NOT NULL DEFAULT false,
    note         text             NOT NULL DEFAULT '',
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_geo_external_stops_due ON geo_external_stops (next_try_at) WHERE pushed_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_geo_external_stops_location ON geo_external_stops (location_id, state);
CREATE INDEX IF NOT EXISTS idx_geo_external_stops_expires ON geo_external_stops (expires_at);

CREATE TABLE IF NOT EXISTS geo_external_work (
    order_id          uuid        PRIMARY KEY REFERENCES geo_external_stops(order_id) ON DELETE CASCADE,
    address           jsonb       NOT NULL,
    address_key       text        NOT NULL DEFAULT '',
    candidates        jsonb,
    confirmed_address jsonb,
    created_at        timestamptz NOT NULL DEFAULT now(),
    discard_at        timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_geo_external_work_discard ON geo_external_work (discard_at);
-- +goose StatementEnd
