CREATE TABLE tenants (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    slug          text        NOT NULL UNIQUE,
    name          text        NOT NULL,
    config        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    username      text,
    password_hash text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    store_id      bigint
);
CREATE UNIQUE INDEX idx_tenants_username ON tenants (username) WHERE username IS NOT NULL;
CREATE UNIQUE INDEX idx_tenants_store_id ON tenants (store_id) WHERE store_id IS NOT NULL;

CREATE TABLE locations (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid        NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    slug              text        NOT NULL,
    name              text        NOT NULL,
    timezone          text        NOT NULL DEFAULT 'America/New_York',
    daily_reset_hour  smallint    NOT NULL DEFAULT 0,
    phone             text        NOT NULL DEFAULT '',
    address_line1     text        NOT NULL DEFAULT '',
    address_line2     text        NOT NULL DEFAULT '',
    config            jsonb       NOT NULL DEFAULT '{}'::jsonb,
    store_type        text        NOT NULL DEFAULT 'restaurant',
    created_at        timestamptz NOT NULL DEFAULT now(),
    -- 00013_geo: store position, delivery radius, published offline map.
    lat               double precision,
    lng               double precision,
    geo_source        text        NOT NULL DEFAULT '',
    delivery_radius_m integer     NOT NULL DEFAULT 8047,
    map_url           text        NOT NULL DEFAULT '',
    map_sha256        text        NOT NULL DEFAULT '',
    map_version       text        NOT NULL DEFAULT '',
    map_bbox          double precision[] NOT NULL DEFAULT '{}',
    UNIQUE (tenant_id, slug),
    UNIQUE (id, tenant_id)
);
-- One tenant = one restaurant = one location (migration 00003).
CREATE UNIQUE INDEX idx_locations_one_per_tenant ON locations (tenant_id);

CREATE TABLE printers (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    name        text        NOT NULL,
    ip          text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    is_receipt  boolean     NOT NULL DEFAULT false,
    mac         text        NOT NULL DEFAULT '',
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE,
    UNIQUE (location_id, name)
);
CREATE INDEX idx_printers_location ON printers(location_id);
-- One receipt printer per location (migration 00004).
CREATE UNIQUE INDEX idx_printers_one_receipt ON printers (location_id) WHERE is_receipt;

CREATE TABLE devices (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid        NOT NULL,
    location_id   uuid        NOT NULL,
    name          text        NOT NULL,
    kind          text        NOT NULL DEFAULT 'agent',
    key_hash      text        NOT NULL UNIQUE,
    key_prefix    text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz,
    revoked_at    timestamptz,
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE RESTRICT
);
CREATE INDEX idx_devices_active ON devices(key_hash) WHERE revoked_at IS NULL;
CREATE INDEX idx_devices_location ON devices(location_id);

CREATE TABLE orders (
    id                    uuid        PRIMARY KEY,
    tenant_id             uuid        NOT NULL,
    location_id           uuid        NOT NULL,
    schema_version        integer     NOT NULL DEFAULT 1,
    source                text        NOT NULL,
    source_event_id       text        NOT NULL,
    kitchen_order_number  integer,
    status                text        NOT NULL,
    source_status         text        NOT NULL DEFAULT '',
    currency              text        NOT NULL,

    subtotal_cents        bigint      NOT NULL,
    tax_cents             bigint      NOT NULL,
    tip_cents             bigint      NOT NULL,
    total_cents           bigint      NOT NULL,
    fees                  jsonb       NOT NULL DEFAULT '[]'::jsonb,
    discounts             jsonb       NOT NULL DEFAULT '[]'::jsonb,

    customer_display_name text        NOT NULL DEFAULT '',
    customer_email        text        NOT NULL DEFAULT '',
    customer_phone        text        NOT NULL DEFAULT '',
    customer_external_id  text        NOT NULL DEFAULT '',

    fulfillment_type      text        NOT NULL,
    fulfillment_address   jsonb,
    promised_at           timestamptz,
    table_id              text        NOT NULL DEFAULT '',

    notes                 text        NOT NULL DEFAULT '',
    source_metadata       jsonb       NOT NULL DEFAULT '{}'::jsonb,
    raw_payload           bytea       NOT NULL,

    placed_at             timestamptz NOT NULL,
    received_at           timestamptz NOT NULL,
    updated_at            timestamptz NOT NULL,
    -- 00005_floor: mirrored from the agent's floor events.
    estimated_ready_at    timestamptz,
    driver_name           text        NOT NULL DEFAULT '',
    -- 00007: newest agent floor event applied (stale events can't overwrite fields).
    floor_at              timestamptz,
    -- 00009: agent ack (resend until stored) + the agent's floor status for restore.
    agent_ack_at          timestamptz,
    agent_status          jsonb,
    agent_status_at       timestamptz,
    agent_batch_id        bigint,

    UNIQUE (tenant_id, source, source_event_id),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE RESTRICT
);
CREATE INDEX idx_orders_received_at ON orders(received_at DESC);
CREATE INDEX idx_orders_location_received ON orders(location_id, received_at DESC);
CREATE INDEX idx_orders_unacked ON orders (received_at) WHERE agent_ack_at IS NULL;
CREATE INDEX idx_orders_agent_batch ON orders (location_id, agent_batch_id);

CREATE TABLE line_items (
    id                uuid     PRIMARY KEY,
    order_id          uuid     NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    parent_id         uuid              REFERENCES line_items(id) ON DELETE CASCADE,
    position          integer  NOT NULL,
    sku               text     NOT NULL DEFAULT '',
    name              text     NOT NULL,
    quantity          integer  NOT NULL,
    unit_price_cents  bigint   NOT NULL,
    line_total_cents  bigint   NOT NULL,
    notes             text     NOT NULL DEFAULT '',
    -- migration 00004: per-line kitchen routing stamped at ingest
    size              text     NOT NULL DEFAULT '',
    menu_item_id      uuid,
    printer_id        uuid,
    routing           text     NOT NULL DEFAULT ''
);
CREATE INDEX idx_line_items_order_id ON line_items(order_id);
CREATE INDEX idx_line_items_parent_id ON line_items(parent_id);

CREATE TABLE payments (
    id            uuid        PRIMARY KEY,
    order_id      uuid        NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    processor     text        NOT NULL,
    status        text        NOT NULL,
    amount_cents  bigint      NOT NULL,
    token         text        NOT NULL DEFAULT '',
    last4         text        NOT NULL DEFAULT '',
    captured_at   timestamptz,
    auth_code     text        NOT NULL DEFAULT ''
);
CREATE INDEX idx_payments_order_id ON payments(order_id);

CREATE TABLE status_events (
    order_id    uuid        NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    position    integer     NOT NULL,
    from_status text        NOT NULL DEFAULT '',
    to_status   text        NOT NULL,
    at          timestamptz NOT NULL,
    actor       text        NOT NULL,
    actor_id    uuid,
    reason      text        NOT NULL DEFAULT '',
    PRIMARY KEY (order_id, position)
);

CREATE TABLE outbox_events (
    id              uuid        PRIMARY KEY,
    aggregate_id    uuid        NOT NULL,
    tenant_id       uuid        NOT NULL,
    location_id     uuid        NOT NULL,
    type            text        NOT NULL,
    payload         jsonb       NOT NULL DEFAULT '{}'::jsonb,
    occurred_at     timestamptz NOT NULL,
    schema_version  integer     NOT NULL DEFAULT 1
);
CREATE INDEX idx_outbox_events_occurred_at ON outbox_events(occurred_at);
CREATE INDEX idx_outbox_events_aggregate_id ON outbox_events(aggregate_id);
CREATE INDEX idx_outbox_events_scope ON outbox_events(tenant_id, location_id);

CREATE TABLE outbox_dispatches (
    event_id         uuid        NOT NULL REFERENCES outbox_events(id) ON DELETE CASCADE,
    handler_name     text        NOT NULL,
    dispatched_at    timestamptz,
    attempts         integer     NOT NULL DEFAULT 0,
    last_error       text        NOT NULL DEFAULT '',
    last_attempt_at  timestamptz,
    next_attempt_at  timestamptz,
    PRIMARY KEY (event_id, handler_name)
);
CREATE INDEX idx_outbox_dispatches_pending
    ON outbox_dispatches (handler_name, next_attempt_at NULLS FIRST)
    WHERE dispatched_at IS NULL;

-- ─── Menu module (per-location catalog) ─────────────────────────────────────

CREATE TABLE menus (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    name        text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    available   boolean     NOT NULL DEFAULT true,
    position    integer     NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE,
    UNIQUE (id, tenant_id)
);
CREATE INDEX idx_menus_location ON menus(location_id);

CREATE TABLE menu_categories (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid        NOT NULL,
    location_id        uuid        NOT NULL,
    menu_id            uuid        NOT NULL,
    name               text        NOT NULL,
    description        text        NOT NULL DEFAULT '',
    available          boolean     NOT NULL DEFAULT true,
    position           integer     NOT NULL DEFAULT 0,
    default_printer_id uuid,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (menu_id, tenant_id)     REFERENCES menus(id, tenant_id)      ON DELETE CASCADE,
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id)  ON DELETE CASCADE,
    FOREIGN KEY (default_printer_id)     REFERENCES printers(id)              ON DELETE SET NULL,
    UNIQUE (id, tenant_id)
);
CREATE INDEX idx_menu_categories_menu ON menu_categories(menu_id);

CREATE TABLE menu_items (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid        NOT NULL,
    location_id   uuid        NOT NULL,
    category_id   uuid        NOT NULL,
    name          text        NOT NULL,
    description   text        NOT NULL DEFAULT '',
    price_cents   bigint      NOT NULL DEFAULT 0,
    tax_rate_bps  integer     NOT NULL DEFAULT 0,
    in_stock      boolean     NOT NULL DEFAULT true,
    out_until     timestamptz,
    tags          jsonb       NOT NULL DEFAULT '[]'::jsonb,
    printer_id    uuid,
    position      integer     NOT NULL DEFAULT 0,
    attributes    jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (category_id, tenant_id) REFERENCES menu_categories(id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id)       ON DELETE CASCADE,
    FOREIGN KEY (printer_id)             REFERENCES printers(id)                   ON DELETE SET NULL,
    UNIQUE (id, tenant_id)
);
CREATE INDEX idx_menu_items_category ON menu_items(category_id);

CREATE TABLE modifier_groups (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      uuid        NOT NULL,
    location_id    uuid        NOT NULL,
    name           text        NOT NULL,
    required       boolean     NOT NULL DEFAULT false,
    min_select     integer     NOT NULL DEFAULT 0,
    max_select     integer     NOT NULL DEFAULT 0,
    allow_multiple boolean     NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE,
    UNIQUE (id, tenant_id)
);
CREATE INDEX idx_modifier_groups_location ON modifier_groups(location_id);

CREATE TABLE modifier_options (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid        NOT NULL,
    location_id       uuid        NOT NULL,
    group_id          uuid        NOT NULL,
    name              text        NOT NULL,
    price_delta_cents bigint      NOT NULL DEFAULT 0,
    in_stock          boolean     NOT NULL DEFAULT true,
    position          integer     NOT NULL DEFAULT 0,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (group_id, tenant_id) REFERENCES modifier_groups(id, tenant_id) ON DELETE CASCADE,
    UNIQUE (id, tenant_id)
);
CREATE INDEX idx_modifier_options_group ON modifier_options(group_id);

CREATE TABLE item_modifier_groups (
    tenant_id uuid    NOT NULL,
    item_id   uuid    NOT NULL,
    group_id  uuid    NOT NULL,
    position  integer NOT NULL DEFAULT 0,
    FOREIGN KEY (item_id, tenant_id)  REFERENCES menu_items(id, tenant_id)      ON DELETE CASCADE,
    FOREIGN KEY (group_id, tenant_id) REFERENCES modifier_groups(id, tenant_id) ON DELETE CASCADE,
    PRIMARY KEY (item_id, group_id)
);
CREATE INDEX idx_item_modifier_groups_group ON item_modifier_groups(group_id);

-- ─── Operators (call-center order-takers; read-only menu access) ────────────

CREATE TABLE operators (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    username      text        NOT NULL UNIQUE,
    password_hash text        NOT NULL,
    display_name  text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE operator_locations (
    operator_id uuid NOT NULL REFERENCES operators(id) ON DELETE CASCADE,
    tenant_id   uuid NOT NULL,
    location_id uuid NOT NULL,
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE,
    PRIMARY KEY (operator_id, location_id)
);
CREATE INDEX idx_operator_locations_op ON operator_locations(operator_id);

CREATE TABLE operator_tokens (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    operator_id  uuid        NOT NULL REFERENCES operators(id) ON DELETE CASCADE,
    token_hash   text        NOT NULL UNIQUE,
    token_prefix text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    expires_at   timestamptz,
    revoked_at   timestamptz
);
CREATE INDEX idx_operator_tokens_hash ON operator_tokens(token_hash);

-- 00005_floor: agent floor-event idempotency + the mirrored driver roster.
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

CREATE TABLE drivers (
    id          uuid        NOT NULL,
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    name        text        NOT NULL,
    position    integer     NOT NULL,
    status      text        NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE,
    PRIMARY KEY (location_id, id) -- 00007
);
CREATE INDEX idx_drivers_location ON drivers (location_id, position);

-- 00006_driver_shifts: driver shifts mirrored from the agent.
CREATE TABLE driver_shifts (
    id          uuid        NOT NULL,
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    driver_id   uuid        NOT NULL,
    driver_name text        NOT NULL,
    clock_in    timestamptz NOT NULL,
    clock_out   timestamptz,
    auto_out    boolean     NOT NULL DEFAULT false,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE,
    PRIMARY KEY (location_id, id) -- 00007
);
CREATE INDEX idx_driver_shifts_location ON driver_shifts (location_id, clock_in DESC);

-- 00008_app_releases: Quibase APKs (console uploads, agents download).
CREATE TABLE app_releases (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    app          text        NOT NULL,
    version_code bigint      NOT NULL,
    version_name text        NOT NULL,
    sha256       text        NOT NULL,
    size         bigint      NOT NULL,
    apk          bytea       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (app, version_code)
);

-- 00009_agent_link: agent floor state, health, nightly DB backups.
CREATE TABLE agent_state (
    location_id uuid        PRIMARY KEY,
    tenant_id   uuid        NOT NULL,
    state       jsonb       NOT NULL,
    at          timestamptz NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);

CREATE TABLE agent_health (
    location_id uuid        PRIMARY KEY,
    tenant_id   uuid        NOT NULL,
    device_id   uuid,
    health      jsonb       NOT NULL,
    at          timestamptz NOT NULL,
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);

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

-- 00010_diagnostics: tablet report tokens + troubleshooting reports.
CREATE TABLE tablet_report_tokens (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid        NOT NULL,
    location_id  uuid        NOT NULL,
    tablet_id    text        NOT NULL,
    tablet_name  text        NOT NULL DEFAULT '',
    token_hash   text        NOT NULL UNIQUE,
    key_prefix   text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at   timestamptz,
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_tablet_report_tokens_active ON tablet_report_tokens (location_id) WHERE revoked_at IS NULL;

CREATE TABLE diag_reports (
    id            uuid        PRIMARY KEY,
    tenant_id     uuid        NOT NULL,
    location_id   uuid        NOT NULL,
    tablet_id     text        NOT NULL DEFAULT '',
    tablet_name   text        NOT NULL DEFAULT '',
    via           text        NOT NULL CHECK (via IN ('pi', 'direct')),
    kind          text        NOT NULL,
    scenario      text        NOT NULL DEFAULT '',
    outcome       text        NOT NULL,
    headline      text        NOT NULL DEFAULT '',
    cause         text        NOT NULL DEFAULT '',
    fixed_by      text        NOT NULL DEFAULT '',
    app_version   text        NOT NULL DEFAULT '',
    agent_version text        NOT NULL DEFAULT '',
    report        jsonb       NOT NULL,
    created_at    timestamptz NOT NULL,
    received_at   timestamptz NOT NULL DEFAULT now(),
    paged_at      timestamptz,
    resolved_at   timestamptz,
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_diag_reports_location ON diag_reports (location_id, received_at DESC);
CREATE INDEX idx_diag_reports_received ON diag_reports (received_at DESC);

-- 00012_pi_updates_printer_follow: printer MACs + IP history, Pi update log.
CREATE TABLE printer_ip_changes (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    printer_id  uuid        NOT NULL REFERENCES printers(id) ON DELETE CASCADE,
    old_ip      text        NOT NULL DEFAULT '',
    new_ip      text        NOT NULL DEFAULT '',
    mac         text        NOT NULL DEFAULT '',
    source      text        NOT NULL CHECK (source IN ('agent', 'manager')),
    at          timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_printer_ip_changes_location ON printer_ip_changes (location_id, at DESC);

CREATE TABLE agent_updates (
    id           uuid        PRIMARY KEY,
    tenant_id    uuid        NOT NULL,
    location_id  uuid        NOT NULL,
    action       text        NOT NULL,
    requested_by text        NOT NULL DEFAULT '',
    force        boolean     NOT NULL DEFAULT false,
    state        text        NOT NULL,
    from_version text        NOT NULL DEFAULT '',
    to_version   text        NOT NULL DEFAULT '',
    error        text        NOT NULL DEFAULT '',
    requested_at timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_agent_updates_location ON agent_updates (location_id, requested_at DESC);
CREATE INDEX idx_agent_updates_requested ON agent_updates (requested_at DESC);

-- 00013_geo: geocode cache, per-order geo + push state, driver runs.
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
    -- 00014: address candidates (state 'check') + the address staff picked.
    candidates        jsonb,
    confirmed_address jsonb,
    -- 00015: the pending result is staff's own pin / choice (order.geo confirmed).
    confirmed         boolean          NOT NULL DEFAULT false
);
CREATE INDEX idx_order_geo_due ON order_geo (next_try_at) WHERE pushed_at IS NULL;
CREATE INDEX idx_order_geo_location ON order_geo (location_id, state);

-- 00014_geo_locate: external stops (POS deliveries, RPC geo.locate).
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
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE,
    -- 00015: the pending result is staff's own pin / choice (order.geo confirmed).
    confirmed    boolean          NOT NULL DEFAULT false
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
