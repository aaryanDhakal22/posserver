-- +goose Up
-- +goose StatementBegin
-- Reporting views for Grafana (observability/TENANT-SCOPING.md §2, 2026-10-26).
--
-- Schema `reporting` holds read-only views over the order tables with NO
-- customer PII: no customer name / phone / email / external id, no
-- fulfillment address, no notes, no raw payload, no payments. Grafana reads
-- them as role `grafana_ro`, which gets USAGE on `reporting` + SELECT on its
-- views only (never on `public`). The views run with their owner's rights
-- (security_invoker off, the default), so grafana_ro needs no grant on the
-- underlying tables. The role is created per database by the operator
-- (observability/postgres/grafana-ro.sql); this migration only grants to it,
-- and only when it already exists.
--
-- Column names are a contract the dashboards are built against: add columns at
-- the end in a new migration (CREATE OR REPLACE VIEW keeps grants), never
-- rename or drop one.
CREATE SCHEMA IF NOT EXISTS reporting;

-- One row per store (tenant = restaurant = one location).
CREATE VIEW reporting.stores AS
SELECT t.id        AS tenant_id,
       t.slug      AS tenant,
       t.name      AS store_name,
       l.id        AS location_id,
       l.slug      AS location,
       l.timezone  AS timezone
  FROM tenants t
  JOIN locations l ON l.tenant_id = t.id;

-- One row per online order. Stage times are the first status_events.at with
-- that to_status (the agent's floor events, mirrored by main). Durations are
-- NULL until both ends exist. `late`: ready after the promised ready time, or
-- still not ready (received / validated / scheduled / accepted / preparing)
-- and past it; false when no estimate was given or the order ended without a
-- ready time (cancelled, rejected, out or completed straight from cooking).
CREATE VIEW reporting.orders AS
SELECT o.id                    AS order_id,
       t.slug                  AS tenant,
       l.slug                  AS location,
       t.name                  AS store_name,
       l.timezone              AS timezone,
       o.source                AS source,
       o.fulfillment_type      AS fulfillment_type,
       o.status                AS status,
       o.kitchen_order_number  AS kitchen_order_number,
       o.subtotal_cents        AS subtotal_cents,
       o.tip_cents             AS tip_cents,
       o.total_cents           AS total_cents,
       o.placed_at             AS placed_at,
       o.received_at           AS received_at,
       o.promised_at           AS promised_at,
       o.estimated_ready_at    AS estimated_ready_at,
       st.accepted_at          AS accepted_at,
       st.ready_at             AS ready_at,
       st.out_at               AS out_at,
       st.completed_at         AS completed_at,
       st.cancelled_at         AS cancelled_at,
       NULLIF(o.driver_name, '') AS driver_name,
       (EXTRACT(EPOCH FROM (o.estimated_ready_at - st.accepted_at)) / 60)::double precision AS prep_minutes_promised,
       EXTRACT(EPOCH FROM (st.accepted_at - o.received_at))::double precision              AS accept_seconds,
       (EXTRACT(EPOCH FROM (st.ready_at - st.accepted_at)) / 60)::double precision         AS prep_minutes_actual,
       (EXTRACT(EPOCH FROM (st.out_at - st.ready_at)) / 60)::double precision              AS wait_out_minutes,
       CASE WHEN o.fulfillment_type = 'delivery'
            THEN (EXTRACT(EPOCH FROM (st.completed_at - st.out_at)) / 60)::double precision
       END                                                                                 AS delivery_minutes,
       (EXTRACT(EPOCH FROM (st.completed_at - o.received_at)) / 60)::double precision      AS total_minutes,
       CASE
           WHEN o.estimated_ready_at IS NULL THEN false
           WHEN st.ready_at IS NOT NULL THEN st.ready_at > o.estimated_ready_at
           WHEN o.status IN ('received', 'validated', 'scheduled', 'accepted', 'preparing')
               THEN now() > o.estimated_ready_at
           ELSE false
       END                                                                                 AS late,
       (o.received_at AT TIME ZONE l.timezone)::date                                       AS business_date
  FROM orders o
  JOIN tenants t   ON t.id = o.tenant_id
  JOIN locations l ON l.id = o.location_id
  LEFT JOIN LATERAL (
      SELECT min(se.at) FILTER (WHERE se.to_status = 'accepted')    AS accepted_at,
             min(se.at) FILTER (WHERE se.to_status = 'ready')       AS ready_at,
             min(se.at) FILTER (WHERE se.to_status = 'on_delivery') AS out_at,
             min(se.at) FILTER (WHERE se.to_status = 'completed')   AS completed_at,
             min(se.at) FILTER (WHERE se.to_status = 'cancelled')   AS cancelled_at
        FROM status_events se
       WHERE se.order_id = o.id
  ) st ON true;

-- Driver clock-in / clock-out, as mirrored from the agent's driver.shift events.
CREATE VIEW reporting.driver_shifts AS
SELECT t.slug         AS tenant,
       l.slug         AS location,
       ds.driver_name AS driver_name,
       ds.clock_in    AS started_at,
       ds.clock_out   AS ended_at,
       ds.auto_out    AS auto_out
  FROM driver_shifts ds
  JOIN tenants t   ON t.id = ds.tenant_id
  JOIN locations l ON l.id = ds.location_id;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'grafana_ro') THEN
        GRANT USAGE ON SCHEMA reporting TO grafana_ro;
        GRANT SELECT ON ALL TABLES IN SCHEMA reporting TO grafana_ro;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP SCHEMA IF EXISTS reporting CASCADE;
-- +goose StatementEnd
