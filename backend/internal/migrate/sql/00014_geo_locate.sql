-- +goose Up
-- +goose StatementBegin
-- POS deliveries + address candidates (main 2.1.0, 2026-10-25).
--
-- order_geo: a new state 'check' (the address is ambiguous: up to 3
--   candidates are pushed and staff pick one, RPC geo.choose), the candidates
--   themselves (so choose can resolve the index) and the address staff
--   confirmed (re-pushed with the result until the agent acks it).
ALTER TABLE order_geo DROP CONSTRAINT IF EXISTS order_geo_state_check;
ALTER TABLE order_geo ADD CONSTRAINT order_geo_state_check
    CHECK (state IN ('pending', 'located', 'check', 'not_found'));
ALTER TABLE order_geo ADD COLUMN candidates jsonb;
ALTER TABLE order_geo ADD COLUMN confirmed_address jsonb;

-- geo_external_stops: a delivery main has no order for (a POS check pulled by
--   the store hub; RPC geo.locate). order_id is the agent's own id. A point
--   and the push state only, never address text; dropped by geo.forget (void,
--   batch close) or at expires_at (24 h), so online and POS deliveries share
--   one drive-time table for AUTO.
CREATE TABLE geo_external_stops (
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
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_geo_external_stops_due ON geo_external_stops (next_try_at) WHERE pushed_at IS NULL;
CREATE INDEX idx_geo_external_stops_location ON geo_external_stops (location_id, state);
CREATE INDEX idx_geo_external_stops_expires ON geo_external_stops (expires_at);

-- geo_external_work: the address text of an external stop, held only while
--   main needs it — until it is located (or given up), or until staff pick a
--   candidate and the agent acks the confirmed address — and never past
--   discard_at (6 h after the locate request).
CREATE TABLE geo_external_work (
    order_id          uuid        PRIMARY KEY REFERENCES geo_external_stops(order_id) ON DELETE CASCADE,
    address           jsonb       NOT NULL,
    address_key       text        NOT NULL DEFAULT '',
    candidates        jsonb,
    confirmed_address jsonb,
    created_at        timestamptz NOT NULL DEFAULT now(),
    discard_at        timestamptz NOT NULL
);
CREATE INDEX idx_geo_external_work_discard ON geo_external_work (discard_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS geo_external_work;
DROP TABLE IF EXISTS geo_external_stops;
UPDATE order_geo SET state = 'pending', candidates = NULL WHERE state = 'check';
ALTER TABLE order_geo DROP COLUMN IF EXISTS confirmed_address;
ALTER TABLE order_geo DROP COLUMN IF EXISTS candidates;
ALTER TABLE order_geo DROP CONSTRAINT IF EXISTS order_geo_state_check;
ALTER TABLE order_geo ADD CONSTRAINT order_geo_state_check
    CHECK (state IN ('pending', 'located', 'not_found'));
-- +goose StatementEnd
