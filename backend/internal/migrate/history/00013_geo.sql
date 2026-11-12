-- +goose Up
-- +goose StatementBegin
-- Delivery maps + driver runs (main 2.0.9, 2026-10-22).
--
-- locations: the store's own position (geocoded from its address, or a pin a
--   manager dragged — geo_source 'manual' is never overwritten by a geocode),
--   the delivery radius ("outside delivery area" past it, default 5 mi) and
--   the published offline map file the Pi downloads (map_url '' = none).
--   map_bbox is [minLng, minLat, maxLng, maxLat].
ALTER TABLE locations ADD COLUMN lat double precision;
ALTER TABLE locations ADD COLUMN lng double precision;
ALTER TABLE locations ADD COLUMN geo_source text NOT NULL DEFAULT '';
ALTER TABLE locations ADD COLUMN delivery_radius_m integer NOT NULL DEFAULT 8047;
ALTER TABLE locations ADD COLUMN map_url text NOT NULL DEFAULT '';
ALTER TABLE locations ADD COLUMN map_sha256 text NOT NULL DEFAULT '';
ALTER TABLE locations ADD COLUMN map_version text NOT NULL DEFAULT '';
ALTER TABLE locations ADD COLUMN map_bbox double precision[] NOT NULL DEFAULT '{}';

-- geo_cache: every address main has located, keyed by its normalized form
--   (line1|city|state|zip). A pinned row (staff dropped a pin on the tablet)
--   always wins over a geocode and is never overwritten by one.
CREATE TABLE geo_cache (
    address_key text             PRIMARY KEY,
    lat         double precision NOT NULL,
    lng         double precision NOT NULL,
    quality     text             NOT NULL,
    source      text             NOT NULL DEFAULT '',
    pinned      boolean          NOT NULL DEFAULT false,
    updated_at  timestamptz      NOT NULL DEFAULT now()
);

-- order_geo: one row per delivery order, written right after ingest (state
--   'pending') and worked by the geo worker: geocode → route from the store →
--   drive times to the other open deliveries → RPC order.geo to the agent.
--   pushed_at NULL = the agent has not acked the latest result yet (re-pushed
--   when it reconnects). next_try_at drives both geocode retries and pushes.
CREATE TABLE order_geo (
    order_id     uuid             PRIMARY KEY REFERENCES orders(id) ON DELETE CASCADE,
    tenant_id    uuid             NOT NULL,
    location_id  uuid             NOT NULL,
    state        text             NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'located', 'not_found')),
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
    updated_at   timestamptz      NOT NULL DEFAULT now()
);
CREATE INDEX idx_order_geo_due ON order_geo (next_try_at) WHERE pushed_at IS NULL;
CREATE INDEX idx_order_geo_location ON order_geo (location_id, state);

-- delivery_runs: every run a driver was sent out on (agent floor event
--   delivery.run) — AUTO's suggestion vs what was actually sent, kept past the
--   Pi's batch retention for tuning. id = the floor event id (idempotent).
CREATE TABLE delivery_runs (
    id            uuid        PRIMARY KEY,
    tenant_id     uuid        NOT NULL,
    location_id   uuid        NOT NULL,
    batch_id      bigint,
    driver_id     text        NOT NULL DEFAULT '',
    driver_name   text        NOT NULL DEFAULT '',
    plan_id       text        NOT NULL DEFAULT '',
    suggested_ids jsonb       NOT NULL DEFAULT '[]'::jsonb,
    sent_ids      jsonb       NOT NULL DEFAULT '[]'::jsonb,
    stops         jsonb       NOT NULL DEFAULT '[]'::jsonb,
    at            timestamptz NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_delivery_runs_location ON delivery_runs (location_id, at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS delivery_runs;
DROP TABLE IF EXISTS order_geo;
DROP TABLE IF EXISTS geo_cache;
ALTER TABLE locations DROP COLUMN IF EXISTS map_bbox;
ALTER TABLE locations DROP COLUMN IF EXISTS map_version;
ALTER TABLE locations DROP COLUMN IF EXISTS map_sha256;
ALTER TABLE locations DROP COLUMN IF EXISTS map_url;
ALTER TABLE locations DROP COLUMN IF EXISTS delivery_radius_m;
ALTER TABLE locations DROP COLUMN IF EXISTS geo_source;
ALTER TABLE locations DROP COLUMN IF EXISTS lng;
ALTER TABLE locations DROP COLUMN IF EXISTS lat;
-- +goose StatementEnd
