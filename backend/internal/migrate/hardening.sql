-- The 2026-11-12 hardening, as a delta over the old migration chain's head
-- (history/00048). The baseline (sql/00001_baseline.sql) already contains all
-- of it; this file exists for two things only:
--   1. adopting a dev / gate database that ran the old chain (migrate.adopt):
--      chain head + this file = the baseline, then goose's history is
--      rewritten to "00001 applied";
--   2. the proof: TestBaselineEqualsChain builds both and compares pg_dump.
-- Never edit it after the squash: new schema changes are new migrations.

-- ─── Roles ───────────────────────────────────────────────────────────────────
-- Group roles (NOLOGIN) the grants below name. The deploy creates them as the
-- superuser before migrating (with the api's login IN ROLE quicc_app); a dev
-- or gate database, migrated by the superuser, gets them here.
DO $roles$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'quicc_app') THEN
        CREATE ROLE quicc_app NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'quicc_readonly') THEN
        CREATE ROLE quicc_readonly NOLOGIN;
    END IF;
END $roles$;

-- ─── Functions: a fixed search_path and qualified names (pg_restore runs with
-- an empty search_path; 00043's lesson) ──────────────────────────────────────
CREATE OR REPLACE FUNCTION public.bump_menu_version() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog, public AS $f$
DECLARE
    loc uuid;
BEGIN
    IF TG_TABLE_NAME = 'item_modifier_groups' THEN
        SELECT location_id INTO loc FROM public.menu_items WHERE id = COALESCE(NEW.item_id, OLD.item_id);
    ELSIF TG_OP = 'DELETE' THEN
        loc := OLD.location_id;
    ELSE
        loc := NEW.location_id;
    END IF;
    IF loc IS NOT NULL AND EXISTS (SELECT 1 FROM public.locations WHERE id = loc) THEN
        INSERT INTO public.menu_versions (location_id, version, changed_at) VALUES (loc, 2, now())
        ON CONFLICT (location_id) DO UPDATE SET version = public.menu_versions.version + 1, changed_at = now();
    END IF;
    RETURN NULL;
END;
$f$;

CREATE OR REPLACE FUNCTION public.card_holds_on_order_cancel() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog, public AS $f$
BEGIN
    IF NEW.status IN ('cancelled', 'rejected') AND OLD.status IS DISTINCT FROM NEW.status THEN
        UPDATE public.card_holds SET cancel_requested_at = COALESCE(cancel_requested_at, now()),
            refund_requested_at = CASE WHEN status = 'captured' THEN COALESCE(refund_requested_at, now()) ELSE refund_requested_at END,
            next_try_at = now(), attempts = 0, updated_at = now()
        WHERE order_id = NEW.id AND status IN ('held', 'captured');
    END IF;
    RETURN NEW;
END $f$;

CREATE OR REPLACE FUNCTION public.customer_addresses_touch() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog, public AS $f$
BEGIN
    UPDATE public.customers SET updated_at = now() WHERE id = COALESCE(NEW.customer_id, OLD.customer_id);
    RETURN NULL;
END $f$;

CREATE OR REPLACE FUNCTION public.customers_tombstone() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog, public AS $f$
BEGIN
    INSERT INTO public.customer_tombstones (customer_id, location_id) VALUES (OLD.id, OLD.location_id)
    ON CONFLICT (customer_id) DO UPDATE SET deleted_at = now();
    RETURN OLD;
END $f$;

CREATE OR REPLACE FUNCTION public.customers_touch() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog, public AS $f$
BEGIN
    IF (NEW.phone, NEW.name, NEW.first_name, NEW.last_name, NEW.notes, NEW.source)
       IS DISTINCT FROM (OLD.phone, OLD.name, OLD.first_name, OLD.last_name, OLD.notes, OLD.source) THEN
        NEW.updated_at = now();
    END IF;
    RETURN NEW;
END $f$;

CREATE OR REPLACE FUNCTION public.payments_fill_location() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog, public AS $f$
BEGIN
    IF NEW.location_id IS NULL THEN
        SELECT location_id INTO NEW.location_id FROM public.orders WHERE id = NEW.order_id;
    END IF;
    RETURN NEW;
END $f$;

-- ─── Constraints ─────────────────────────────────────────────────────────────
-- Every payment belongs to a store (the trigger above fills it from the
-- order); the store can't be deleted under its money.
UPDATE payments p SET location_id = o.location_id FROM orders o WHERE p.location_id IS NULL AND o.id = p.order_id;
ALTER TABLE payments ALTER COLUMN location_id SET NOT NULL;
ALTER TABLE payments ADD CONSTRAINT payments_location_id_fkey
    FOREIGN KEY (location_id) REFERENCES locations(id) ON DELETE RESTRICT;

-- ─── Indexes (EXPLAIN on 6 months of Lombardi's-scale volume, 2026-11-12) ────
-- Lookups the plans scanned for:
CREATE INDEX idx_card_holds_payment ON card_holds (payment_id) WHERE payment_id IS NOT NULL;
CREATE INDEX idx_card_holds_tip_payment ON card_holds (tip_payment_id) WHERE tip_payment_id IS NOT NULL;
CREATE INDEX idx_card_holds_ref ON card_holds (location_id, payment_ref);
CREATE INDEX idx_payments_location_received ON payments (location_id, received_at);
CREATE INDEX idx_orders_phone ON orders (location_id, customer_phone) WHERE customer_phone <> '';
CREATE INDEX idx_orders_source_event ON orders (source_event_id);
-- The child side of foreign keys (deletes / SET NULL scan them):
CREATE INDEX idx_menu_categories_location ON menu_categories (location_id);
CREATE INDEX idx_menu_categories_printer ON menu_categories (default_printer_id) WHERE default_printer_id IS NOT NULL;
CREATE INDEX idx_menu_items_location ON menu_items (location_id);
CREATE INDEX idx_menu_items_printer ON menu_items (printer_id) WHERE printer_id IS NOT NULL;
CREATE INDEX idx_operator_locations_location ON operator_locations (location_id);
CREATE INDEX idx_operator_tokens_operator ON operator_tokens (operator_id);
CREATE INDEX idx_printer_ip_changes_printer ON printer_ip_changes (printer_id);
CREATE INDEX idx_channels_view ON channels (view_id) WHERE view_id IS NOT NULL;
CREATE INDEX idx_channel_orders_operator ON channel_orders (operator_id) WHERE operator_id IS NOT NULL;
CREATE INDEX idx_drawer_movements_location ON drawer_movements (location_id, at);
CREATE INDEX idx_drawer_session_revisions_location ON drawer_session_revisions (location_id);
-- Duplicates of a unique constraint's own index:
DROP INDEX idx_operator_tokens_hash;   -- = operator_tokens_token_hash_key
DROP INDEX idx_devices_active;         -- = devices_key_hash_key
DROP INDEX idx_printers_location;      -- = printers_location_id_name_key

-- ─── The baseline marker ─────────────────────────────────────────────────────
-- One row: which baseline this database was built from (a restore is checked
-- against it: migrate.CheckBaseline / deploy/backup).
CREATE TABLE schema_baseline (
    id            smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    baseline      text        NOT NULL,
    squashed_from integer     NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);
INSERT INTO schema_baseline (baseline, squashed_from) VALUES ('2026-11-12', 48);

-- ─── Audit log (append-only) ─────────────────────────────────────────────────
-- Who changed what: DB triggers on money / staff / settings / menu tables
-- (source 'db': the row before and after, secrets masked) and main's own row
-- per admin / manager write (source 'app': the route, the answer). The actor
-- comes from the connection's quicc.actor setting, which main sets from the
-- request (auditconn); none = 'system'. Rows are never updated or deleted:
-- the app role has INSERT + SELECT only and a trigger refuses the rest.
CREATE TABLE audit_log (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at          timestamptz NOT NULL DEFAULT clock_timestamp(),
    tenant_id   uuid,
    location_id uuid,
    actor_kind  text        NOT NULL DEFAULT 'system', -- admin | manager | device | operator | channel | customer | platform | system
    actor_id    text        NOT NULL DEFAULT '',
    actor_name  text        NOT NULL DEFAULT '',
    source      text        NOT NULL CHECK (source IN ('db', 'app')),
    action      text        NOT NULL,                  -- db: insert | update | delete; app: the method
    entity      text        NOT NULL,                  -- db: the table; app: the route
    entity_id   text        NOT NULL DEFAULT '',
    before      jsonb,
    after       jsonb,
    detail      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    trace_id    text        NOT NULL DEFAULT '',
    request_id  text        NOT NULL DEFAULT '',
    ip          text        NOT NULL DEFAULT '',
    db_user     text        NOT NULL DEFAULT current_user,
    tx_id       bigint      NOT NULL DEFAULT txid_current()
);
CREATE INDEX idx_audit_log_location ON audit_log (location_id, id DESC);
CREATE INDEX idx_audit_log_entity ON audit_log (entity, entity_id);
CREATE INDEX idx_audit_log_at ON audit_log USING brin (at);
CREATE INDEX idx_audit_log_trace ON audit_log (trace_id) WHERE trace_id <> '';

CREATE FUNCTION public.audit_log_append_only() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog, public AS $f$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only (% refused)', TG_OP USING ERRCODE = 'insufficient_privilege';
END $f$;
CREATE TRIGGER audit_log_append_only BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION public.audit_log_append_only();
CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION public.audit_log_append_only();

-- audit_row(args…): one audit_log row per changed row. Arguments:
--   'key=a,b'       the entity id columns (default id)
--   'mask=a,b'      secrets: never copied; a change shows as "(changed)"
--   'ignore=a,b'    noise: not compared, not copied (an update changing only these logs nothing)
--   'only=a,b'      copy / compare only these columns (+ the key)
CREATE FUNCTION public.audit_row() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog, public AS $f$
DECLARE
    keys   text[] := ARRAY['id'];
    masked text[] := '{}';
    ignored text[] := '{}';
    only_cols text[];
    arg    text;
    o      jsonb;
    n      jsonb;
    b      jsonb;
    a      jsonb;
    r      jsonb;
    changed text[] := '{}';
    actor  jsonb;
    ent_id text;
BEGIN
    FOR i IN 0 .. TG_NARGS - 1 LOOP
        arg := TG_ARGV[i];
        IF arg LIKE 'key=%' THEN keys := string_to_array(substr(arg, 5), ',');
        ELSIF arg LIKE 'mask=%' THEN masked := string_to_array(substr(arg, 6), ',');
        ELSIF arg LIKE 'ignore=%' THEN ignored := string_to_array(substr(arg, 8), ',');
        ELSIF arg LIKE 'only=%' THEN only_cols := string_to_array(substr(arg, 6), ',');
        END IF;
    END LOOP;
    IF TG_OP <> 'INSERT' THEN o := to_jsonb(OLD); END IF;
    IF TG_OP <> 'DELETE' THEN n := to_jsonb(NEW); END IF;
    r := COALESCE(n, o);
    SELECT string_agg(r ->> u.c, '/' ORDER BY u.i) INTO ent_id FROM unnest(keys) WITH ORDINALITY u(c, i);
    IF only_cols IS NOT NULL THEN
        IF o IS NOT NULL THEN
            SELECT COALESCE(jsonb_object_agg(e.key, e.value), '{}') INTO o FROM jsonb_each(o) e
            WHERE e.key = ANY (only_cols) OR e.key = ANY (keys);
        END IF;
        IF n IS NOT NULL THEN
            SELECT COALESCE(jsonb_object_agg(e.key, e.value), '{}') INTO n FROM jsonb_each(n) e
            WHERE e.key = ANY (only_cols) OR e.key = ANY (keys);
        END IF;
    END IF;
    IF TG_OP = 'UPDATE' THEN
        SELECT COALESCE(array_agg(e.key), '{}') INTO changed FROM jsonb_each(n) e
        WHERE NOT (e.key = ANY (ignored)) AND (o -> e.key) IS DISTINCT FROM e.value;
        IF cardinality(changed) = 0 THEN
            RETURN NULL;
        END IF;
        SELECT jsonb_object_agg(c, CASE WHEN c = ANY (masked) THEN to_jsonb('(changed)'::text) ELSE o -> c END),
               jsonb_object_agg(c, CASE WHEN c = ANY (masked) THEN to_jsonb('(changed)'::text) ELSE n -> c END)
          INTO b, a FROM unnest(changed) c;
    ELSIF TG_OP = 'INSERT' THEN
        a := n - masked - ignored;
    ELSE
        b := o - masked - ignored;
    END IF;
    actor := NULLIF(current_setting('quicc.actor', true), '')::jsonb;
    INSERT INTO public.audit_log (tenant_id, location_id, actor_kind, actor_id, actor_name, source, action,
        entity, entity_id, before, after, detail, trace_id, request_id, ip)
    VALUES (
        COALESCE((r ->> 'tenant_id')::uuid, CASE WHEN TG_TABLE_NAME = 'tenants' THEN (r ->> 'id')::uuid END,
                 (actor ->> 't')::uuid),
        COALESCE((r ->> 'location_id')::uuid, CASE WHEN TG_TABLE_NAME = 'locations' THEN (r ->> 'id')::uuid END,
                 (actor ->> 'l')::uuid),
        COALESCE(actor ->> 'k', 'system'), COALESCE(actor ->> 'id', ''), COALESCE(actor ->> 'n', ''),
        'db', lower(TG_OP), TG_TABLE_NAME, COALESCE(ent_id, ''), b, a,
        CASE WHEN actor ? 'by' THEN jsonb_build_object('impersonated_by', actor ->> 'by') ELSE '{}'::jsonb END,
        COALESCE(current_setting('quicc.trace', true), ''), COALESCE(actor ->> 'rid', ''), COALESCE(actor ->> 'ip', ''));
    RETURN NULL;
END $f$;

-- Money: every payment / refund / void row, card holds, drawers, Zs, flags;
-- an order's money and its cancel / un-cancel (not every floor step: the
-- status history is status_events); deletes of the money history tables.
CREATE TRIGGER audit_payments AFTER INSERT OR UPDATE OR DELETE ON payments
    FOR EACH ROW EXECUTE FUNCTION public.audit_row();
CREATE TRIGGER audit_orders_money AFTER UPDATE ON orders FOR EACH ROW
    WHEN (OLD.subtotal_cents IS DISTINCT FROM NEW.subtotal_cents OR OLD.tax_cents IS DISTINCT FROM NEW.tax_cents
          OR OLD.tip_cents IS DISTINCT FROM NEW.tip_cents OR OLD.total_cents IS DISTINCT FROM NEW.total_cents
          OR OLD.fees IS DISTINCT FROM NEW.fees OR OLD.discounts IS DISTINCT FROM NEW.discounts
          OR (OLD.status IS DISTINCT FROM NEW.status
              AND (NEW.status IN ('cancelled', 'rejected') OR OLD.status IN ('cancelled', 'rejected'))))
    EXECUTE FUNCTION public.audit_row('only=status,subtotal_cents,tax_cents,tip_cents,total_cents,fees,discounts,tenant_id,location_id');
CREATE TRIGGER audit_orders_delete AFTER DELETE ON orders FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('only=source,source_event_id,status,subtotal_cents,tax_cents,tip_cents,total_cents,fees,discounts,placed_at,agent_batch_id,tenant_id,location_id');
CREATE TRIGGER audit_card_holds AFTER INSERT OR UPDATE OR DELETE ON card_holds FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('key=order_id', 'ignore=updated_at,next_try_at,attempts,last_error');
CREATE TRIGGER audit_drawer_sessions AFTER INSERT OR UPDATE OR DELETE ON drawer_sessions FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('ignore=updated_at');
CREATE TRIGGER audit_drawer_movements AFTER INSERT OR UPDATE OR DELETE ON drawer_movements FOR EACH ROW
    EXECUTE FUNCTION public.audit_row();
CREATE TRIGGER audit_z_reports AFTER INSERT OR UPDATE OR DELETE ON z_reports FOR EACH ROW
    EXECUTE FUNCTION public.audit_row();
CREATE TRIGGER audit_money_flags AFTER INSERT OR UPDATE OR DELETE ON money_flags FOR EACH ROW
    EXECUTE FUNCTION public.audit_row();
CREATE TRIGGER audit_order_money_changes AFTER DELETE ON order_money_changes FOR EACH ROW
    EXECUTE FUNCTION public.audit_row();
CREATE TRIGGER audit_z_report_versions AFTER DELETE ON z_report_versions FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('key=seq');
CREATE TRIGGER audit_drawer_session_revisions AFTER DELETE ON drawer_session_revisions FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('key=seq');
CREATE TRIGGER audit_staff_journal AFTER DELETE ON staff_journal FOR EACH ROW
    EXECUTE FUNCTION public.audit_row();

-- People, permissions, credentials, store settings.
CREATE TRIGGER audit_staff AFTER INSERT OR UPDATE OR DELETE ON staff FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('mask=pin_hash', 'ignore=updated_at');
CREATE TRIGGER audit_terminal_settings AFTER INSERT OR UPDATE OR DELETE ON terminal_settings FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('key=location_id', 'ignore=updated_at,version');
CREATE TRIGGER audit_store_ordering AFTER INSERT OR UPDATE OR DELETE ON store_ordering FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('key=location_id', 'ignore=updated_at,restocked_at');
CREATE TRIGGER audit_store_hours_exceptions AFTER INSERT OR UPDATE OR DELETE ON store_hours_exceptions FOR EACH ROW
    EXECUTE FUNCTION public.audit_row();
CREATE TRIGGER audit_pricing_settings AFTER INSERT OR UPDATE OR DELETE ON pricing_settings FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('key=location_id,kind', 'ignore=updated_at');
CREATE TRIGGER audit_tenants AFTER INSERT OR UPDATE OR DELETE ON tenants FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('mask=password_hash');
CREATE TRIGGER audit_locations AFTER INSERT OR UPDATE OR DELETE ON locations FOR EACH ROW
    EXECUTE FUNCTION public.audit_row();
CREATE TRIGGER audit_printers AFTER INSERT OR UPDATE OR DELETE ON printers FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('ignore=updated_at');
CREATE TRIGGER audit_devices AFTER INSERT OR UPDATE OR DELETE ON devices FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('mask=key_hash', 'ignore=last_used_at');
CREATE TRIGGER audit_channels AFTER INSERT OR UPDATE OR DELETE ON channels FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('mask=key_hash', 'ignore=last_used_at,updated_at');
CREATE TRIGGER audit_platform_keys AFTER INSERT OR UPDATE OR DELETE ON platform_keys FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('mask=key_hash', 'ignore=last_used_at');
CREATE TRIGGER audit_operators AFTER INSERT OR UPDATE OR DELETE ON operators FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('mask=password_hash', 'ignore=updated_at');
CREATE TRIGGER audit_operator_locations AFTER INSERT OR UPDATE OR DELETE ON operator_locations FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('key=operator_id,location_id');
CREATE TRIGGER audit_operator_tokens AFTER INSERT OR UPDATE OR DELETE ON operator_tokens FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('mask=token_hash', 'ignore=last_used_at');
CREATE TRIGGER audit_tablet_report_tokens AFTER INSERT OR UPDATE OR DELETE ON tablet_report_tokens FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('mask=token_hash', 'ignore=last_used_at');
CREATE TRIGGER audit_square_connections AFTER INSERT OR UPDATE OR DELETE ON square_connections FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('key=location_id', 'mask=access_token_enc,refresh_token_enc', 'ignore=refreshed_at,expires_at');
CREATE TRIGGER audit_site_settings AFTER INSERT OR UPDATE OR DELETE ON site_settings FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('key=location_id', 'ignore=updated_at');
CREATE TRIGGER audit_site_domains AFTER INSERT OR UPDATE OR DELETE ON site_domains FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('key=host');

-- The menu and its prices (the layout is large: a change shows as "(changed)").
CREATE TRIGGER audit_menus AFTER INSERT OR UPDATE OR DELETE ON menus FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('ignore=updated_at');
CREATE TRIGGER audit_menu_categories AFTER INSERT OR UPDATE OR DELETE ON menu_categories FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('ignore=updated_at');
CREATE TRIGGER audit_menu_items AFTER INSERT OR UPDATE OR DELETE ON menu_items FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('ignore=updated_at');
CREATE TRIGGER audit_menu_sizes AFTER INSERT OR UPDATE OR DELETE ON menu_sizes FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('ignore=updated_at');
CREATE TRIGGER audit_menu_item_sizes AFTER INSERT OR UPDATE OR DELETE ON menu_item_sizes FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('key=item_id,size_id');
CREATE TRIGGER audit_menu_families AFTER INSERT OR UPDATE OR DELETE ON menu_families FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('ignore=updated_at');
CREATE TRIGGER audit_modifier_groups AFTER INSERT OR UPDATE OR DELETE ON modifier_groups FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('ignore=updated_at');
CREATE TRIGGER audit_modifier_options AFTER INSERT OR UPDATE OR DELETE ON modifier_options FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('ignore=updated_at');
CREATE TRIGGER audit_modifier_option_prices AFTER INSERT OR UPDATE OR DELETE ON modifier_option_prices FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('key=option_id,size_id');
CREATE TRIGGER audit_item_modifier_groups AFTER INSERT OR UPDATE OR DELETE ON item_modifier_groups FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('key=item_id,group_id');
CREATE TRIGGER audit_deals AFTER INSERT OR UPDATE OR DELETE ON deals FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('ignore=updated_at');
CREATE TRIGGER audit_menu_views AFTER INSERT OR UPDATE OR DELETE ON menu_views FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('mask=layout', 'ignore=updated_at');
CREATE TRIGGER audit_menu_view_entries AFTER INSERT OR UPDATE OR DELETE ON menu_view_entries FOR EACH ROW
    EXECUTE FUNCTION public.audit_row('ignore=updated_at');

-- ─── Privileges ──────────────────────────────────────────────────────────────
-- quicc_app: rows only (no TRUNCATE / REFERENCES / TRIGGER, never DDL: it owns
-- nothing); goose's table + the marker read-only; the audit log INSERT +
-- SELECT. quicc_readonly: SELECT. Default privileges carry this to tables a
-- later migration (run by the same migrator) creates.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO quicc_app, quicc_readonly;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO quicc_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO quicc_app;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO quicc_readonly;
REVOKE INSERT, UPDATE, DELETE ON audit_log, schema_baseline FROM quicc_app;
GRANT INSERT ON audit_log TO quicc_app;
DO $goose$
BEGIN
    IF to_regclass('public.goose_db_version') IS NOT NULL THEN
        REVOKE INSERT, UPDATE, DELETE ON public.goose_db_version FROM quicc_app;
    END IF;
END $goose$;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO quicc_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO quicc_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO quicc_readonly;
