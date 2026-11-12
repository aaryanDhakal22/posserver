-- +goose Up
-- +goose StatementBegin
-- Hardening C1 (HARDENING-PLAN.md §1–§2, 2026-11-12): money records survive
-- deletes, payments carry their meaning, Z reports and closed drawers keep
-- their history, and main flags (never drops) the money the Pi sends.
--
-- Every new CHECK is added NOT VALID (new rows are checked at once) and then
-- validated in its own guarded block: a database that already holds a
-- breaking row keeps booting (a WARNING names the constraint; the old rows
-- stay unchecked until someone fixes them and runs VALIDATE CONSTRAINT).

-- S-1: stores. A test store (created with test: true; on dev every store
-- unless it says test: false) may be deleted with its money; any other store
-- with money records is retired instead (retired_at, devices revoked).
ALTER TABLE tenants ADD COLUMN test_store boolean NOT NULL DEFAULT false,
                    ADD COLUMN retired_at timestamptz;

-- F-4: a deleted device's key hash, so the Pi hears "device_revoked" (not
-- "unknown_key") for a key whose device or store was deleted.
CREATE TABLE device_tombstones (
    key_hash    text        PRIMARY KEY,
    device_id   uuid        NOT NULL,
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    kind        text        NOT NULL,
    deleted_at  timestamptz NOT NULL DEFAULT now()
);

-- S-1: money rows never go with a delete by cascade: whatever deletes an
-- order / store deletes them on purpose (test stores, dev) or is refused.
ALTER TABLE payments DROP CONSTRAINT payments_order_id_fkey,
    ADD CONSTRAINT payments_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT;
ALTER TABLE card_holds DROP CONSTRAINT card_holds_order_id_fkey,
    ADD CONSTRAINT card_holds_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT;
ALTER TABLE card_holds DROP CONSTRAINT card_holds_location_id_tenant_id_fkey,
    ADD CONSTRAINT card_holds_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE RESTRICT;
ALTER TABLE channel_orders DROP CONSTRAINT channel_orders_order_id_fkey,
    ADD CONSTRAINT channel_orders_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE RESTRICT;
ALTER TABLE z_reports DROP CONSTRAINT z_reports_location_id_tenant_id_fkey,
    ADD CONSTRAINT z_reports_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE RESTRICT;
ALTER TABLE drawer_sessions DROP CONSTRAINT drawer_sessions_location_id_tenant_id_fkey,
    ADD CONSTRAINT drawer_sessions_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE RESTRICT;
ALTER TABLE drawer_movements DROP CONSTRAINT drawer_movements_location_id_tenant_id_fkey,
    ADD CONSTRAINT drawer_movements_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE RESTRICT;
ALTER TABLE staff_journal DROP CONSTRAINT staff_journal_location_id_tenant_id_fkey,
    ADD CONSTRAINT staff_journal_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE RESTRICT;
ALTER TABLE order_placements DROP CONSTRAINT order_placements_channel_id_fkey,
    ADD CONSTRAINT order_placements_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE RESTRICT;

-- S-2 / M-4: a cancelled card hold's payment is voided (it was written as
-- "refunded" with a positive amount); a refund is always negative.
UPDATE payments SET status = 'voided' WHERE status = 'refunded' AND amount_cents >= 0;
ALTER TABLE payments
    ADD COLUMN refund_of   uuid REFERENCES payments(id),
    ADD COLUMN received_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN location_id uuid,
    ADD COLUMN client_key  text        NOT NULL DEFAULT '',
    ADD COLUMN origin      text        NOT NULL DEFAULT '',
    ADD COLUMN batch_id    bigint,
    ADD COLUMN approved_by text        NOT NULL DEFAULT '',
    ADD COLUMN flags       text[]      NOT NULL DEFAULT '{}';
UPDATE payments p SET location_id = o.location_id FROM orders o WHERE o.id = p.order_id;
-- Every writer gets the location for free (the vendor / channel inserts
-- don't know the column).
CREATE FUNCTION payments_fill_location() RETURNS trigger LANGUAGE plpgsql SET search_path = public AS $f$
BEGIN
    IF NEW.location_id IS NULL THEN
        SELECT location_id INTO NEW.location_id FROM public.orders WHERE id = NEW.order_id;
    END IF;
    RETURN NEW;
END $f$;
CREATE TRIGGER payments_fill_location BEFORE INSERT ON payments FOR EACH ROW EXECUTE FUNCTION payments_fill_location();
ALTER TABLE payments
    ADD CONSTRAINT payments_status_ck CHECK (status IN ('pending', 'authorized', 'captured', 'failed', 'refunded', 'voided')) NOT VALID,
    ADD CONSTRAINT payments_tip_ck CHECK (tip_cents >= 0) NOT VALID,
    ADD CONSTRAINT payments_refund_sign_ck CHECK ((status = 'refunded') = (amount_cents < 0)) NOT VALID;
-- Payment idempotency: one client_key per store; one counter payment per
-- processor reference (a Square payment recorded twice under two ids).
CREATE UNIQUE INDEX payments_client_key_uq ON payments (location_id, client_key) WHERE client_key <> '';
CREATE INDEX idx_payments_refund_of ON payments (refund_of) WHERE refund_of IS NOT NULL;
CREATE INDEX idx_payments_counter_batch ON payments (location_id, batch_id) WHERE origin = 'counter';

-- M-3 / M-4: a hold knows its payment row; a cancel / refund is asked for in
-- the cancelling transaction and done by the worker (with retries).
ALTER TABLE card_holds DROP CONSTRAINT card_holds_status_check,
    ADD CONSTRAINT card_holds_status_check CHECK (status IN ('held', 'captured', 'cancelled', 'failed', 'refunded'));
ALTER TABLE card_holds
    ADD COLUMN payment_id          uuid REFERENCES payments(id),
    ADD COLUMN cancel_requested_at timestamptz,
    ADD COLUMN refund_requested_at timestamptz,
    ADD COLUMN refunded_cents      bigint NOT NULL DEFAULT 0,
    ADD COLUMN refund_ref          text   NOT NULL DEFAULT '',
    ADD COLUMN tip_payment_id      uuid REFERENCES payments(id);
UPDATE card_holds h SET payment_id = (
    SELECT p.id FROM payments p WHERE p.order_id = h.order_id AND p.processor = 'card'
    ORDER BY (p.token = h.payment_ref) DESC, p.id LIMIT 1);
ALTER TABLE card_holds
    ADD CONSTRAINT card_holds_money_ck CHECK (amount_cents >= 0 AND tip_cents >= 0 AND added_tip_cents >= 0
        AND captured_cents >= 0 AND refunded_cents >= 0) NOT VALID,
    ADD CONSTRAINT card_holds_captured_ck CHECK (status <> 'captured' OR captured_at IS NOT NULL) NOT VALID;
CREATE INDEX idx_card_holds_undo ON card_holds (next_try_at)
    WHERE cancel_requested_at IS NOT NULL OR refund_requested_at IS NOT NULL;
-- M-3: an order that becomes cancelled / rejected asks its hold to be
-- voided (held) or refunded (captured) in the same transaction, whatever
-- path cancelled it (a floor cancel, a void, not_delivered); the card-hold
-- worker does it, with retries.
CREATE FUNCTION card_holds_on_order_cancel() RETURNS trigger LANGUAGE plpgsql SET search_path = public AS $f$
BEGIN
    IF NEW.status IN ('cancelled', 'rejected') AND OLD.status IS DISTINCT FROM NEW.status THEN
        UPDATE public.card_holds SET cancel_requested_at = COALESCE(cancel_requested_at, now()),
            refund_requested_at = CASE WHEN status = 'captured' THEN COALESCE(refund_requested_at, now()) ELSE refund_requested_at END,
            next_try_at = now(), attempts = 0, updated_at = now()
        WHERE order_id = NEW.id AND status IN ('held', 'captured');
    END IF;
    RETURN NEW;
END $f$;
CREATE TRIGGER card_holds_on_order_cancel AFTER UPDATE OF status ON orders
    FOR EACH ROW EXECUTE FUNCTION card_holds_on_order_cancel();

-- S-3 / S-4: orders and line items.
ALTER TABLE orders
    ADD CONSTRAINT orders_money_ck CHECK (subtotal_cents >= 0 AND tax_cents >= 0 AND tip_cents >= 0 AND total_cents >= 0) NOT VALID,
    ADD CONSTRAINT orders_status_ck CHECK (status IN ('received', 'validated', 'scheduled', 'accepted', 'preparing', 'ready',
        'on_delivery', 'completed', 'rejected', 'cancelled')) NOT VALID,
    ADD CONSTRAINT orders_fulfillment_ck CHECK (fulfillment_type IN ('pickup', 'delivery', 'dine_in', 'counter')) NOT VALID,
    ADD CONSTRAINT orders_source_ck CHECK (source IN ('online', 'mobile', 'pos', 'kiosk', 'counter', 'web', 'operator', 'vendor')) NOT VALID;
ALTER TABLE line_items ADD CONSTRAINT line_items_quantity_ck CHECK (quantity > 0) NOT VALID;
ALTER TABLE drawer_sessions ADD CONSTRAINT drawer_sessions_close_reason_ck
    CHECK (close_reason IN ('', 'counted', 'handover', 'batch_close')) NOT VALID;
ALTER TABLE floor_events_seen ADD CONSTRAINT floor_events_seen_result_ck
    CHECK (result IN ('applied', 'duplicate', 'stale', 'not_found', 'invalid', 'conflict')) NOT VALID;

-- S-3: the money inside orders.fees / discounts and the Z's headline totals
-- as columns (generated: no writer can forget them). Reports query these.
-- Schema-qualified: pg_restore runs these with an empty search_path.
CREATE FUNCTION money_cents(v text) RETURNS bigint LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $f$
    SELECT CASE WHEN v ~ '^-?[0-9]{1,18}$' THEN v::bigint END $f$;
CREATE FUNCTION money_fee_cents(fees jsonb, typ text, field text) RETURNS bigint LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $f$
    SELECT COALESCE(sum(COALESCE(public.money_cents(f ->> field), 0)), 0)::bigint
    FROM jsonb_array_elements(CASE WHEN jsonb_typeof(fees) = 'array' THEN fees ELSE '[]'::jsonb END) f
    WHERE typ = '' OR f ->> 'Type' = typ $f$;
ALTER TABLE orders
    ADD COLUMN delivery_fee_cents bigint GENERATED ALWAYS AS (money_fee_cents(fees, 'delivery', 'Amount')) STORED,
    ADD COLUMN driver_fee_cents   bigint GENERATED ALWAYS AS (money_fee_cents(fees, 'delivery', 'DriverCents')) STORED,
    ADD COLUMN service_fee_cents  bigint GENERATED ALWAYS AS (money_fee_cents(fees, 'service', 'Amount')) STORED,
    ADD COLUMN card_fee_cents     bigint GENERATED ALWAYS AS (money_fee_cents(fees, 'card', 'Amount')) STORED,
    ADD COLUMN discount_cents     bigint GENERATED ALWAYS AS (money_fee_cents(discounts, '', 'Amount')) STORED,
    -- M-12: the newest order.changed applied (an older one is stale).
    ADD COLUMN money_changed_at   timestamptz;

-- M-12: every money change of an order (order.changed), before and after.
CREATE TABLE order_money_changes (
    id          bigserial   PRIMARY KEY,
    order_id    uuid        NOT NULL,
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    event_id    uuid        NOT NULL,
    at          timestamptz NOT NULL,
    before      jsonb       NOT NULL,
    after       jsonb       NOT NULL,
    after_z     boolean     NOT NULL DEFAULT false,
    received_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_order_money_changes_order ON order_money_changes (order_id, id);

-- M-9 / M-12: the Z keeps its versions; main checks it against the payments
-- it has (main_check, main_difference_cents); flagged = the Pi's Z doesn't
-- balance or main disagrees.
ALTER TABLE z_reports
    ADD COLUMN version               integer NOT NULL DEFAULT 1,
    ADD COLUMN flagged               boolean NOT NULL DEFAULT false,
    ADD COLUMN main_check            jsonb,
    ADD COLUMN main_difference_cents bigint,
    ADD COLUMN sales_total_cents     bigint GENERATED ALWAYS AS (money_cents(report #>> '{sales,total_cents}')) STORED,
    ADD COLUMN sales_tax_cents       bigint GENERATED ALWAYS AS (money_cents(report #>> '{sales,tax_cents}')) STORED,
    ADD COLUMN tips_card_cents       bigint GENERATED ALWAYS AS (money_cents(report #>> '{tips,card_cents}')) STORED,
    ADD COLUMN tips_cash_cents       bigint GENERATED ALWAYS AS (money_cents(report #>> '{tips,cash_cents}')) STORED,
    ADD COLUMN refunds_cents         bigint GENERATED ALWAYS AS (money_cents(report #>> '{refunds,cents}')) STORED,
    ADD COLUMN over_short_cents      bigint GENERATED ALWAYS AS (money_cents(report #>> '{over_short_cents}')) STORED,
    ADD COLUMN difference_cents      bigint GENERATED ALWAYS AS (money_cents(report #>> '{reconcile,difference_cents}')) STORED;
CREATE TABLE z_report_versions (
    seq           bigserial   PRIMARY KEY,
    id            uuid        NOT NULL,
    tenant_id     uuid        NOT NULL,
    location_id   uuid        NOT NULL,
    batch_id      bigint      NOT NULL,
    version       integer     NOT NULL,
    business_date date        NOT NULL,
    opened_at     timestamptz,
    closed_at     timestamptz,
    report        jsonb       NOT NULL,
    received_at   timestamptz NOT NULL,
    superseded_at timestamptz NOT NULL DEFAULT now(),
    superseded_by uuid        NOT NULL,
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE RESTRICT
);
CREATE INDEX idx_z_report_versions_batch ON z_report_versions (location_id, batch_id, seq);
CREATE INDEX idx_z_report_versions_id ON z_report_versions (location_id, id);

-- M-9: a closed drawer session changed by a later drawer.closed keeps the
-- row it had (as JSON) before the change.
CREATE TABLE drawer_session_revisions (
    seq         bigserial   PRIMARY KEY,
    drawer_id   uuid        NOT NULL,
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    event_id    uuid        NOT NULL,
    old         jsonb       NOT NULL,
    revised_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE RESTRICT
);
CREATE INDEX idx_drawer_session_revisions_drawer ON drawer_session_revisions (drawer_id, seq);

-- M-2 / M-7 / M-12 / M-13: money main stored but doesn't agree with (or
-- couldn't store under its own id): one row per problem, for the owner.
CREATE TABLE money_flags (
    id          bigserial   PRIMARY KEY,
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    at          timestamptz NOT NULL DEFAULT now(),
    event_id    uuid,
    event_type  text        NOT NULL,
    subject     text        NOT NULL DEFAULT '',
    code        text        NOT NULL,
    detail      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    resolved_at timestamptz,
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE RESTRICT
);
CREATE INDEX idx_money_flags_location ON money_flags (location_id, at DESC);

-- M-20: the website's tip cap (X, the store's ordering setting; default $50).
ALTER TABLE store_ordering ADD COLUMN tip_max_cents bigint NOT NULL DEFAULT 5000
    CHECK (tip_max_cents BETWEEN 0 AND 100000);
-- +goose StatementEnd

-- Validate the new CHECKs one by one; a database with an old breaking row
-- boots anyway (the constraint keeps checking new rows).
-- +goose StatementBegin
DO $v$
DECLARE c record;
BEGIN
    FOR c IN SELECT * FROM (VALUES
        ('payments', 'payments_status_ck'), ('payments', 'payments_tip_ck'), ('payments', 'payments_refund_sign_ck'),
        ('card_holds', 'card_holds_money_ck'), ('card_holds', 'card_holds_captured_ck'),
        ('orders', 'orders_money_ck'), ('orders', 'orders_status_ck'), ('orders', 'orders_fulfillment_ck'),
        ('orders', 'orders_source_ck'), ('line_items', 'line_items_quantity_ck'),
        ('drawer_sessions', 'drawer_sessions_close_reason_ck'), ('floor_events_seen', 'floor_events_seen_result_ck')) AS t(tbl, con)
    LOOP
        BEGIN
            EXECUTE format('ALTER TABLE %I VALIDATE CONSTRAINT %I', c.tbl, c.con);
        EXCEPTION WHEN check_violation THEN
            RAISE WARNING '00043: % has rows that break %; left NOT VALID (new rows are checked)', c.tbl, c.con;
        END;
    END LOOP;
    -- S-4: one hold per processor payment (skipped, with a warning, if two exist).
    BEGIN
        CREATE UNIQUE INDEX card_holds_ref_uq ON card_holds (gateway, payment_ref);
    EXCEPTION WHEN unique_violation THEN
        RAISE WARNING '00043: card_holds has a payment_ref twice; card_holds_ref_uq not created';
        CREATE INDEX card_holds_ref_uq ON card_holds (gateway, payment_ref);
    END;
    -- S-2: one counter payment per processor reference.
    BEGIN
        CREATE UNIQUE INDEX payments_counter_ref_uq ON payments (location_id, processor, token)
            WHERE origin = 'counter' AND token <> '' AND amount_cents > 0;
    EXCEPTION WHEN unique_violation THEN
        RAISE WARNING '00043: a counter payment reference is there twice; payments_counter_ref_uq not created';
    END;
END $v$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE store_ordering DROP COLUMN IF EXISTS tip_max_cents;
DROP TABLE IF EXISTS money_flags;
DROP TABLE IF EXISTS drawer_session_revisions;
DROP TABLE IF EXISTS z_report_versions;
ALTER TABLE z_reports DROP COLUMN IF EXISTS version, DROP COLUMN IF EXISTS flagged, DROP COLUMN IF EXISTS main_check,
    DROP COLUMN IF EXISTS main_difference_cents, DROP COLUMN IF EXISTS sales_total_cents, DROP COLUMN IF EXISTS sales_tax_cents,
    DROP COLUMN IF EXISTS tips_card_cents, DROP COLUMN IF EXISTS tips_cash_cents, DROP COLUMN IF EXISTS refunds_cents,
    DROP COLUMN IF EXISTS over_short_cents, DROP COLUMN IF EXISTS difference_cents;
DROP TABLE IF EXISTS order_money_changes;
ALTER TABLE orders DROP COLUMN IF EXISTS delivery_fee_cents, DROP COLUMN IF EXISTS driver_fee_cents,
    DROP COLUMN IF EXISTS service_fee_cents, DROP COLUMN IF EXISTS card_fee_cents, DROP COLUMN IF EXISTS discount_cents,
    DROP COLUMN IF EXISTS money_changed_at;
DROP FUNCTION IF EXISTS money_fee_cents(jsonb, text, text);
DROP FUNCTION IF EXISTS money_cents(text);
ALTER TABLE floor_events_seen DROP CONSTRAINT IF EXISTS floor_events_seen_result_ck;
ALTER TABLE drawer_sessions DROP CONSTRAINT IF EXISTS drawer_sessions_close_reason_ck;
ALTER TABLE line_items DROP CONSTRAINT IF EXISTS line_items_quantity_ck;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_money_ck, DROP CONSTRAINT IF EXISTS orders_status_ck,
    DROP CONSTRAINT IF EXISTS orders_fulfillment_ck, DROP CONSTRAINT IF EXISTS orders_source_ck;
DROP TRIGGER IF EXISTS card_holds_on_order_cancel ON orders;
DROP FUNCTION IF EXISTS card_holds_on_order_cancel();
DROP INDEX IF EXISTS idx_card_holds_undo;
DROP INDEX IF EXISTS card_holds_ref_uq;
ALTER TABLE card_holds DROP CONSTRAINT IF EXISTS card_holds_money_ck, DROP CONSTRAINT IF EXISTS card_holds_captured_ck,
    DROP COLUMN IF EXISTS payment_id, DROP COLUMN IF EXISTS cancel_requested_at, DROP COLUMN IF EXISTS refund_requested_at,
    DROP COLUMN IF EXISTS refunded_cents, DROP COLUMN IF EXISTS refund_ref, DROP COLUMN IF EXISTS tip_payment_id;
UPDATE card_holds SET status = 'cancelled' WHERE status = 'refunded';
ALTER TABLE card_holds DROP CONSTRAINT card_holds_status_check,
    ADD CONSTRAINT card_holds_status_check CHECK (status IN ('held', 'captured', 'cancelled', 'failed'));
DROP INDEX IF EXISTS payments_counter_ref_uq;
DROP INDEX IF EXISTS idx_payments_counter_batch;
DROP INDEX IF EXISTS idx_payments_refund_of;
DROP INDEX IF EXISTS payments_client_key_uq;
DROP TRIGGER IF EXISTS payments_fill_location ON payments;
DROP FUNCTION IF EXISTS payments_fill_location();
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_status_ck, DROP CONSTRAINT IF EXISTS payments_tip_ck,
    DROP CONSTRAINT IF EXISTS payments_refund_sign_ck,
    DROP COLUMN IF EXISTS refund_of, DROP COLUMN IF EXISTS received_at, DROP COLUMN IF EXISTS location_id,
    DROP COLUMN IF EXISTS client_key, DROP COLUMN IF EXISTS origin, DROP COLUMN IF EXISTS batch_id,
    DROP COLUMN IF EXISTS approved_by, DROP COLUMN IF EXISTS flags;
UPDATE payments SET status = 'refunded' WHERE status = 'voided';
ALTER TABLE order_placements DROP CONSTRAINT order_placements_channel_id_fkey,
    ADD CONSTRAINT order_placements_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES channels(id) ON DELETE CASCADE;
ALTER TABLE staff_journal DROP CONSTRAINT staff_journal_location_id_tenant_id_fkey,
    ADD CONSTRAINT staff_journal_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE;
ALTER TABLE drawer_movements DROP CONSTRAINT drawer_movements_location_id_tenant_id_fkey,
    ADD CONSTRAINT drawer_movements_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE;
ALTER TABLE drawer_sessions DROP CONSTRAINT drawer_sessions_location_id_tenant_id_fkey,
    ADD CONSTRAINT drawer_sessions_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE;
ALTER TABLE z_reports DROP CONSTRAINT z_reports_location_id_tenant_id_fkey,
    ADD CONSTRAINT z_reports_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE;
ALTER TABLE channel_orders DROP CONSTRAINT channel_orders_order_id_fkey,
    ADD CONSTRAINT channel_orders_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE CASCADE;
ALTER TABLE card_holds DROP CONSTRAINT card_holds_location_id_tenant_id_fkey,
    ADD CONSTRAINT card_holds_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE;
ALTER TABLE card_holds DROP CONSTRAINT card_holds_order_id_fkey,
    ADD CONSTRAINT card_holds_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE CASCADE;
ALTER TABLE payments DROP CONSTRAINT payments_order_id_fkey,
    ADD CONSTRAINT payments_order_id_fkey FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE CASCADE;
DROP TABLE IF EXISTS device_tombstones;
ALTER TABLE tenants DROP COLUMN IF EXISTS test_store, DROP COLUMN IF EXISTS retired_at;
-- +goose StatementEnd
