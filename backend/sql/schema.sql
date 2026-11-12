-- main's schema, for sqlc and for reading: the migrations applied to an empty
-- database (today: internal/migrate/sql/00001_baseline.sql verbatim).
-- migrate.TestSchemaFile checks it; after adding a migration, append the same
-- statements here (unqualified names are fine for sqlc).

DO $roles$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'quicc_app') THEN
        CREATE ROLE quicc_app NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'quicc_readonly') THEN
        CREATE ROLE quicc_readonly NOLOGIN;
    END IF;
END $roles$;

-- function audit_log_append_only()
CREATE FUNCTION public.audit_log_append_only() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only (% refused)', TG_OP USING ERRCODE = 'insufficient_privilege';
END $$;

-- function audit_row()
CREATE FUNCTION public.audit_row() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
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
END $$;

-- function bump_menu_version()
CREATE FUNCTION public.bump_menu_version() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
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
$$;

-- function card_holds_on_order_cancel()
CREATE FUNCTION public.card_holds_on_order_cancel() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF NEW.status IN ('cancelled', 'rejected') AND OLD.status IS DISTINCT FROM NEW.status THEN
        UPDATE public.card_holds SET cancel_requested_at = COALESCE(cancel_requested_at, now()),
            refund_requested_at = CASE WHEN status = 'captured' THEN COALESCE(refund_requested_at, now()) ELSE refund_requested_at END,
            next_try_at = now(), attempts = 0, updated_at = now()
        WHERE order_id = NEW.id AND status IN ('held', 'captured');
    END IF;
    RETURN NEW;
END $$;

-- function customer_addresses_touch()
CREATE FUNCTION public.customer_addresses_touch() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    UPDATE public.customers SET updated_at = now() WHERE id = COALESCE(NEW.customer_id, OLD.customer_id);
    RETURN NULL;
END $$;

-- function customers_tombstone()
CREATE FUNCTION public.customers_tombstone() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    INSERT INTO public.customer_tombstones (customer_id, location_id) VALUES (OLD.id, OLD.location_id)
    ON CONFLICT (customer_id) DO UPDATE SET deleted_at = now();
    RETURN OLD;
END $$;

-- function customers_touch()
CREATE FUNCTION public.customers_touch() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF (NEW.phone, NEW.name, NEW.first_name, NEW.last_name, NEW.notes, NEW.source)
       IS DISTINCT FROM (OLD.phone, OLD.name, OLD.first_name, OLD.last_name, OLD.notes, OLD.source) THEN
        NEW.updated_at = now();
    END IF;
    RETURN NEW;
END $$;

-- function money_cents(text)
CREATE FUNCTION public.money_cents(v text) RETURNS bigint
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    AS $_$
    SELECT CASE WHEN v ~ '^-?[0-9]{1,18}$' THEN v::bigint END $_$;

-- function money_fee_cents(jsonb, text, text)
CREATE FUNCTION public.money_fee_cents(fees jsonb, typ text, field text) RETURNS bigint
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    AS $$
    SELECT COALESCE(sum(COALESCE(public.money_cents(f ->> field), 0)), 0)::bigint
    FROM jsonb_array_elements(CASE WHEN jsonb_typeof(fees) = 'array' THEN fees ELSE '[]'::jsonb END) f
    WHERE typ = '' OR f ->> 'Type' = typ $$;

-- function payments_fill_location()
CREATE FUNCTION public.payments_fill_location() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path TO 'pg_catalog', 'public'
    AS $$
BEGIN
    IF NEW.location_id IS NULL THEN
        SELECT location_id INTO NEW.location_id FROM public.orders WHERE id = NEW.order_id;
    END IF;
    RETURN NEW;
END $$;

-- table agent_backups
CREATE TABLE public.agent_backups (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    device_id uuid,
    size bigint NOT NULL,
    sha256 text NOT NULL,
    data bytea NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table agent_health
CREATE TABLE public.agent_health (
    location_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    device_id uuid,
    health jsonb NOT NULL,
    at timestamp with time zone NOT NULL
);

-- table agent_state
CREATE TABLE public.agent_state (
    location_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    state jsonb NOT NULL,
    at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table agent_updates
CREATE TABLE public.agent_updates (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    action text NOT NULL,
    requested_by text DEFAULT ''::text NOT NULL,
    force boolean DEFAULT false NOT NULL,
    state text NOT NULL,
    from_version text DEFAULT ''::text NOT NULL,
    to_version text DEFAULT ''::text NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    requested_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table app_releases
CREATE TABLE public.app_releases (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    app text NOT NULL,
    version_code bigint NOT NULL,
    version_name text NOT NULL,
    sha256 text NOT NULL,
    size bigint NOT NULL,
    apk bytea NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table audit_log
CREATE TABLE public.audit_log (
    id bigint NOT NULL,
    at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    tenant_id uuid,
    location_id uuid,
    actor_kind text DEFAULT 'system'::text NOT NULL,
    actor_id text DEFAULT ''::text NOT NULL,
    actor_name text DEFAULT ''::text NOT NULL,
    source text NOT NULL,
    action text NOT NULL,
    entity text NOT NULL,
    entity_id text DEFAULT ''::text NOT NULL,
    before jsonb,
    after jsonb,
    detail jsonb DEFAULT '{}'::jsonb NOT NULL,
    trace_id text DEFAULT ''::text NOT NULL,
    request_id text DEFAULT ''::text NOT NULL,
    ip text DEFAULT ''::text NOT NULL,
    db_user text DEFAULT CURRENT_USER NOT NULL,
    tx_id bigint DEFAULT txid_current() NOT NULL,
    CONSTRAINT audit_log_source_check CHECK ((source = ANY (ARRAY['db'::text, 'app'::text])))
);

-- sequence audit_log_id_seq
ALTER TABLE public.audit_log ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.audit_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);

-- table card_holds
CREATE TABLE public.card_holds (
    order_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    gateway text NOT NULL,
    payment_ref text NOT NULL,
    amount_cents bigint NOT NULL,
    tip_cents bigint DEFAULT 0 NOT NULL,
    added_tip_cents bigint DEFAULT 0 NOT NULL,
    status text DEFAULT 'held'::text NOT NULL,
    capture_reason text DEFAULT ''::text NOT NULL,
    due_at timestamp with time zone NOT NULL,
    capture_requested_at timestamp with time zone,
    next_try_at timestamp with time zone DEFAULT now() NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    captured_cents bigint DEFAULT 0 NOT NULL,
    tip_payment_ref text DEFAULT ''::text NOT NULL,
    tip_note text DEFAULT ''::text NOT NULL,
    captured_at timestamp with time zone,
    cancelled_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    payment_id uuid,
    cancel_requested_at timestamp with time zone,
    refund_requested_at timestamp with time zone,
    refunded_cents bigint DEFAULT 0 NOT NULL,
    refund_ref text DEFAULT ''::text NOT NULL,
    tip_payment_id uuid,
    CONSTRAINT card_holds_captured_ck CHECK (((status <> 'captured'::text) OR (captured_at IS NOT NULL))),
    CONSTRAINT card_holds_money_ck CHECK (((amount_cents >= 0) AND (tip_cents >= 0) AND (added_tip_cents >= 0) AND (captured_cents >= 0) AND (refunded_cents >= 0))),
    CONSTRAINT card_holds_status_check CHECK ((status = ANY (ARRAY['held'::text, 'captured'::text, 'cancelled'::text, 'failed'::text, 'refunded'::text])))
);

-- table channel_events
CREATE TABLE public.channel_events (
    id bigint NOT NULL,
    channel_id uuid NOT NULL,
    order_id uuid NOT NULL,
    type text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

-- sequence channel_events_id_seq
CREATE SEQUENCE public.channel_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

-- sequence owned by channel_events_id_seq
ALTER SEQUENCE public.channel_events_id_seq OWNED BY public.channel_events.id;

-- table channel_orders
CREATE TABLE public.channel_orders (
    order_id uuid NOT NULL,
    channel_id uuid,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    client_order_id text DEFAULT ''::text NOT NULL,
    payment jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    track_token text,
    alerted_at timestamp with time zone,
    operator_id uuid,
    customer_id uuid
);

-- table channels
CREATE TABLE public.channels (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    kind text NOT NULL,
    name text NOT NULL,
    key_hash text,
    key_prefix text DEFAULT ''::text NOT NULL,
    permissions jsonb DEFAULT '{}'::jsonb NOT NULL,
    builtin boolean DEFAULT false NOT NULL,
    revoked_at timestamp with time zone,
    last_used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    view_id uuid,
    CONSTRAINT channels_kind_check CHECK ((kind = ANY (ARRAY['web'::text, 'operator'::text, 'counter'::text, 'vendor'::text])))
);

-- table customer_addresses
CREATE TABLE public.customer_addresses (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    customer_id uuid NOT NULL,
    label text DEFAULT ''::text NOT NULL,
    line1 text NOT NULL,
    line2 text DEFAULT ''::text NOT NULL,
    city text DEFAULT ''::text NOT NULL,
    state text DEFAULT ''::text NOT NULL,
    zip text DEFAULT ''::text NOT NULL,
    notes text DEFAULT ''::text NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table customer_cards
CREATE TABLE public.customer_cards (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    customer_id uuid NOT NULL,
    gateway text NOT NULL,
    card_ref text NOT NULL,
    brand text DEFAULT ''::text NOT NULL,
    last4 text DEFAULT ''::text NOT NULL,
    exp_month integer DEFAULT 0 NOT NULL,
    exp_year integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table customer_sessions
CREATE TABLE public.customer_sessions (
    token_hash text NOT NULL,
    customer_id uuid NOT NULL,
    location_id uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table customer_tombstones
CREATE TABLE public.customer_tombstones (
    customer_id uuid NOT NULL,
    location_id uuid NOT NULL,
    deleted_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table customers
CREATE TABLE public.customers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    phone text NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    email text DEFAULT ''::text NOT NULL,
    square_customer_id text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    first_name text DEFAULT ''::text NOT NULL,
    last_name text DEFAULT ''::text NOT NULL,
    notes text DEFAULT ''::text NOT NULL,
    source text DEFAULT 'web'::text NOT NULL,
    name_source text DEFAULT ''::text NOT NULL,
    notes_source text DEFAULT ''::text NOT NULL,
    CONSTRAINT customers_name_source_check CHECK ((name_source = ANY (ARRAY[''::text, 'web'::text, 'counter'::text, 'import'::text]))),
    CONSTRAINT customers_notes_source_check CHECK ((notes_source = ANY (ARRAY[''::text, 'web'::text, 'counter'::text, 'import'::text]))),
    CONSTRAINT customers_source_check CHECK ((source = ANY (ARRAY['web'::text, 'counter'::text, 'import'::text])))
);

-- table deals
CREATE TABLE public.deals (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    kind text NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    active boolean DEFAULT true NOT NULL,
    channel_kinds text[] DEFAULT '{web,operator,counter}'::text[] NOT NULL,
    def jsonb DEFAULT '{}'::jsonb NOT NULL,
    code text DEFAULT ''::text NOT NULL,
    "position" integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT deals_kind_check CHECK ((kind = ANY (ARRAY['bundle'::text, 'mix'::text, 'nth'::text, 'code'::text, 'free_item'::text, 'coupon'::text])))
);

-- table delivery_runs
CREATE TABLE public.delivery_runs (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    batch_id bigint,
    driver_id text DEFAULT ''::text NOT NULL,
    driver_name text DEFAULT ''::text NOT NULL,
    plan_id text DEFAULT ''::text NOT NULL,
    suggested_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    sent_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    stops jsonb DEFAULT '[]'::jsonb NOT NULL,
    at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table device_tombstones
CREATE TABLE public.device_tombstones (
    key_hash text NOT NULL,
    device_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    kind text NOT NULL,
    deleted_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table devices
CREATE TABLE public.devices (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    name text NOT NULL,
    kind text DEFAULT 'agent'::text NOT NULL,
    key_hash text NOT NULL,
    key_prefix text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone,
    revoked_at timestamp with time zone
);

-- table diag_reports
CREATE TABLE public.diag_reports (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    tablet_id text DEFAULT ''::text NOT NULL,
    tablet_name text DEFAULT ''::text NOT NULL,
    via text NOT NULL,
    kind text NOT NULL,
    scenario text DEFAULT ''::text NOT NULL,
    outcome text NOT NULL,
    headline text DEFAULT ''::text NOT NULL,
    cause text DEFAULT ''::text NOT NULL,
    fixed_by text DEFAULT ''::text NOT NULL,
    app_version text DEFAULT ''::text NOT NULL,
    agent_version text DEFAULT ''::text NOT NULL,
    report jsonb NOT NULL,
    created_at timestamp with time zone NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    paged_at timestamp with time zone,
    resolved_at timestamp with time zone,
    CONSTRAINT diag_reports_via_check CHECK ((via = ANY (ARRAY['pi'::text, 'direct'::text])))
);

-- table drawer_movements
CREATE TABLE public.drawer_movements (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    drawer_id uuid NOT NULL,
    kind text NOT NULL,
    amount_cents bigint NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    staff_id text DEFAULT ''::text NOT NULL,
    approved_by text DEFAULT ''::text NOT NULL,
    at timestamp with time zone NOT NULL,
    CONSTRAINT drawer_movements_amount_cents_check CHECK ((amount_cents > 0)),
    CONSTRAINT drawer_movements_kind_check CHECK ((kind = ANY (ARRAY['pay_in'::text, 'pay_out'::text])))
);

-- table drawer_session_revisions
CREATE TABLE public.drawer_session_revisions (
    seq bigint NOT NULL,
    drawer_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    event_id uuid NOT NULL,
    old jsonb NOT NULL,
    revised_at timestamp with time zone DEFAULT now() NOT NULL
);

-- sequence drawer_session_revisions_seq_seq
CREATE SEQUENCE public.drawer_session_revisions_seq_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

-- sequence owned by drawer_session_revisions_seq_seq
ALTER SEQUENCE public.drawer_session_revisions_seq_seq OWNED BY public.drawer_session_revisions.seq;

-- table drawer_sessions
CREATE TABLE public.drawer_sessions (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    device_id text DEFAULT ''::text NOT NULL,
    device_name text DEFAULT ''::text NOT NULL,
    staff_id text DEFAULT ''::text NOT NULL,
    staff_name text DEFAULT ''::text NOT NULL,
    batch_id bigint,
    opened_at timestamp with time zone NOT NULL,
    start_cents bigint NOT NULL,
    handover_from uuid,
    closed_at timestamp with time zone,
    closed_by text DEFAULT ''::text NOT NULL,
    close_reason text DEFAULT ''::text NOT NULL,
    counted_cents bigint,
    denominations jsonb,
    expected_cents bigint,
    over_short_cents bigint,
    flagged boolean DEFAULT false NOT NULL,
    cash_sales_cents bigint DEFAULT 0 NOT NULL,
    cash_refunds_cents bigint DEFAULT 0 NOT NULL,
    cash_tips_cents bigint DEFAULT 0 NOT NULL,
    pay_ins_cents bigint DEFAULT 0 NOT NULL,
    pay_outs_cents bigint DEFAULT 0 NOT NULL,
    no_sales integer DEFAULT 0 NOT NULL,
    payments integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT drawer_sessions_close_reason_ck CHECK ((close_reason = ANY (ARRAY[''::text, 'counted'::text, 'handover'::text, 'batch_close'::text])))
);

-- table driver_shifts
CREATE TABLE public.driver_shifts (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    driver_id uuid NOT NULL,
    driver_name text NOT NULL,
    clock_in timestamp with time zone NOT NULL,
    clock_out timestamp with time zone,
    auto_out boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table drivers
CREATE TABLE public.drivers (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    name text NOT NULL,
    "position" integer NOT NULL,
    status text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table email_receipts
CREATE TABLE public.email_receipts (
    order_id uuid NOT NULL,
    kind text NOT NULL,
    location_id uuid NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    next_try_at timestamp with time zone DEFAULT now() NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    message_id text DEFAULT ''::text NOT NULL,
    sent_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT email_receipts_kind_check CHECK ((kind = ANY (ARRAY['placed'::text, 'final'::text]))),
    CONSTRAINT email_receipts_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'sent'::text, 'failed'::text, 'skipped'::text])))
);

-- table floor_events_seen
CREATE TABLE public.floor_events_seen (
    event_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    order_id uuid,
    type text NOT NULL,
    result text NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT floor_events_seen_result_ck CHECK ((result = ANY (ARRAY['applied'::text, 'duplicate'::text, 'stale'::text, 'not_found'::text, 'invalid'::text, 'conflict'::text])))
);

-- table geo_cache
CREATE TABLE public.geo_cache (
    address_key text NOT NULL,
    lat double precision NOT NULL,
    lng double precision NOT NULL,
    quality text NOT NULL,
    source text DEFAULT ''::text NOT NULL,
    pinned boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table item_modifier_groups
CREATE TABLE public.item_modifier_groups (
    tenant_id uuid NOT NULL,
    item_id uuid NOT NULL,
    group_id uuid NOT NULL,
    "position" integer DEFAULT 0 NOT NULL
);

-- table line_items
CREATE TABLE public.line_items (
    id uuid NOT NULL,
    order_id uuid NOT NULL,
    parent_id uuid,
    "position" integer NOT NULL,
    sku text DEFAULT ''::text NOT NULL,
    name text NOT NULL,
    quantity integer NOT NULL,
    unit_price_cents bigint NOT NULL,
    line_total_cents bigint NOT NULL,
    notes text DEFAULT ''::text NOT NULL,
    size text DEFAULT ''::text NOT NULL,
    menu_item_id uuid,
    printer_id uuid,
    routing text DEFAULT ''::text NOT NULL,
    portion text DEFAULT ''::text NOT NULL,
    mode text DEFAULT ''::text NOT NULL,
    coupon text DEFAULT ''::text NOT NULL,
    coupon_no integer DEFAULT 0 NOT NULL,
    CONSTRAINT line_items_mode_check CHECK ((mode = ANY (ARRAY[''::text, 'add'::text, 'only'::text, 'no'::text, 'specialty'::text]))),
    CONSTRAINT line_items_portion_check CHECK ((portion = ANY (ARRAY[''::text, 'first'::text, 'second'::text]))),
    CONSTRAINT line_items_quantity_ck CHECK ((quantity > 0))
);

-- table locations
CREATE TABLE public.locations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    slug text NOT NULL,
    name text NOT NULL,
    timezone text DEFAULT 'America/New_York'::text NOT NULL,
    daily_reset_hour smallint DEFAULT 0 NOT NULL,
    phone text DEFAULT ''::text NOT NULL,
    address_line1 text DEFAULT ''::text NOT NULL,
    address_line2 text DEFAULT ''::text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    store_type text DEFAULT 'restaurant'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    lat double precision,
    lng double precision,
    geo_source text DEFAULT ''::text NOT NULL,
    delivery_radius_m integer DEFAULT 8047 NOT NULL,
    map_url text DEFAULT ''::text NOT NULL,
    map_sha256 text DEFAULT ''::text NOT NULL,
    map_version text DEFAULT ''::text NOT NULL,
    map_bbox double precision[] DEFAULT '{}'::double precision[] NOT NULL
);

-- table menu_categories
CREATE TABLE public.menu_categories (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    menu_id uuid NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    available boolean DEFAULT true NOT NULL,
    "position" integer DEFAULT 0 NOT NULL,
    default_printer_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    no_kitchen_ticket boolean DEFAULT false NOT NULL
);

-- table menu_families
CREATE TABLE public.menu_families (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    kind text NOT NULL,
    name text NOT NULL,
    class text DEFAULT ''::text NOT NULL,
    "position" integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT menu_families_check CHECK ((((kind = 'item'::text) AND (class = ''::text)) OR ((kind = 'mod'::text) AND (class <> ''::text)))),
    CONSTRAINT menu_families_class_check CHECK ((class = ANY (ARRAY[''::text, 'regular'::text, 'premium'::text, 'unpriced'::text]))),
    CONSTRAINT menu_families_kind_check CHECK ((kind = ANY (ARRAY['item'::text, 'mod'::text]))),
    CONSTRAINT menu_families_name_check CHECK (((length(btrim(name)) >= 1) AND (length(btrim(name)) <= 60)))
);

-- table menu_item_sizes
CREATE TABLE public.menu_item_sizes (
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    item_id uuid NOT NULL,
    size_id uuid NOT NULL,
    price_cents bigint NOT NULL,
    kitchen_name text DEFAULT ''::text NOT NULL,
    "position" integer DEFAULT 0 NOT NULL,
    CONSTRAINT menu_item_sizes_price_cents_check CHECK ((price_cents >= 0))
);

-- table menu_items
CREATE TABLE public.menu_items (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    category_id uuid NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    price_cents bigint DEFAULT 0 NOT NULL,
    tax_rate_bps integer DEFAULT 0 NOT NULL,
    in_stock boolean DEFAULT true NOT NULL,
    out_until timestamp with time zone,
    tags jsonb DEFAULT '[]'::jsonb NOT NULL,
    printer_id uuid,
    "position" integer DEFAULT 0 NOT NULL,
    attributes jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    kitchen_name text DEFAULT ''::text NOT NULL,
    sku text DEFAULT ''::text NOT NULL,
    out_hold boolean DEFAULT false NOT NULL,
    no_kitchen_ticket boolean DEFAULT false NOT NULL,
    family_id uuid
);

-- table menu_sizes
CREATE TABLE public.menu_sizes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    name text NOT NULL,
    short text DEFAULT ''::text NOT NULL,
    "position" integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table menu_versions
CREATE TABLE public.menu_versions (
    location_id uuid NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    changed_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table menu_view_builds
CREATE TABLE public.menu_view_builds (
    view_id uuid NOT NULL,
    menu_version bigint NOT NULL,
    build jsonb NOT NULL,
    built_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table menu_view_entries
CREATE TABLE public.menu_view_entries (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    view_id uuid NOT NULL,
    kind text NOT NULL,
    anchor jsonb DEFAULT '{}'::jsonb NOT NULL,
    display_name text DEFAULT ''::text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    photo text DEFAULT ''::text NOT NULL,
    category text DEFAULT ''::text NOT NULL,
    "position" integer DEFAULT 0 NOT NULL,
    hidden boolean DEFAULT false NOT NULL,
    price_override_cents bigint,
    extra jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT menu_view_entries_kind_check CHECK ((kind = ANY (ARRAY['item'::text, 'merge'::text, 'augment'::text, 'map'::text])))
);

-- table menu_views
CREATE TABLE public.menu_views (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    kind text NOT NULL,
    name text NOT NULL,
    rules jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    layout jsonb,
    CONSTRAINT menu_views_kind_check CHECK ((kind = ANY (ARRAY['web'::text, 'operator'::text, 'counter'::text, 'vendor'::text]))),
    CONSTRAINT menu_views_layout_counter CHECK (((layout IS NULL) OR ((kind = 'counter'::text) AND (jsonb_typeof(layout) = 'object'::text))))
);

-- table menus
CREATE TABLE public.menus (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    available boolean DEFAULT true NOT NULL,
    "position" integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table modifier_groups
CREATE TABLE public.modifier_groups (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    name text NOT NULL,
    required boolean DEFAULT false NOT NULL,
    min_select integer DEFAULT 0 NOT NULL,
    max_select integer DEFAULT 0 NOT NULL,
    allow_multiple boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    portions boolean DEFAULT false NOT NULL
);

-- table modifier_option_prices
CREATE TABLE public.modifier_option_prices (
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    option_id uuid NOT NULL,
    size_id uuid NOT NULL,
    price_cents bigint NOT NULL,
    CONSTRAINT modifier_option_prices_price_cents_check CHECK ((price_cents >= 0))
);

-- table modifier_options
CREATE TABLE public.modifier_options (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    group_id uuid NOT NULL,
    name text NOT NULL,
    price_delta_cents bigint DEFAULT 0 NOT NULL,
    in_stock boolean DEFAULT true NOT NULL,
    "position" integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    kitchen_name text DEFAULT ''::text NOT NULL,
    out_hold boolean DEFAULT false NOT NULL,
    family_id uuid,
    show_on_driver_ticket boolean DEFAULT false NOT NULL
);

-- table money_flags
CREATE TABLE public.money_flags (
    id bigint NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    at timestamp with time zone DEFAULT now() NOT NULL,
    event_id uuid,
    event_type text NOT NULL,
    subject text DEFAULT ''::text NOT NULL,
    code text NOT NULL,
    detail jsonb DEFAULT '{}'::jsonb NOT NULL,
    resolved_at timestamp with time zone
);

-- sequence money_flags_id_seq
CREATE SEQUENCE public.money_flags_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

-- sequence owned by money_flags_id_seq
ALTER SEQUENCE public.money_flags_id_seq OWNED BY public.money_flags.id;

-- table operator_locations
CREATE TABLE public.operator_locations (
    operator_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL
);

-- table operator_tokens
CREATE TABLE public.operator_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    operator_id uuid NOT NULL,
    token_hash text NOT NULL,
    token_prefix text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone,
    expires_at timestamp with time zone,
    revoked_at timestamp with time zone
);

-- table operators
CREATE TABLE public.operators (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    username text NOT NULL,
    password_hash text NOT NULL,
    display_name text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table order_geo
CREATE TABLE public.order_geo (
    order_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    state text DEFAULT 'pending'::text NOT NULL,
    lat double precision,
    lng double precision,
    quality text DEFAULT ''::text NOT NULL,
    distance_m integer DEFAULT 0 NOT NULL,
    duration_s integer DEFAULT 0 NOT NULL,
    polyline text DEFAULT ''::text NOT NULL,
    outside_area boolean DEFAULT false NOT NULL,
    store_to_s integer,
    to_store_s integer,
    attempts integer DEFAULT 0 NOT NULL,
    next_try_at timestamp with time zone DEFAULT now() NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    pushed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    candidates jsonb,
    confirmed_address jsonb,
    confirmed boolean DEFAULT false NOT NULL,
    corrected boolean DEFAULT false NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    CONSTRAINT order_geo_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'located'::text, 'check'::text, 'not_found'::text])))
);

-- table order_money_changes
CREATE TABLE public.order_money_changes (
    id bigint NOT NULL,
    order_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    event_id uuid NOT NULL,
    at timestamp with time zone NOT NULL,
    before jsonb NOT NULL,
    after jsonb NOT NULL,
    after_z boolean DEFAULT false NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL
);

-- sequence order_money_changes_id_seq
CREATE SEQUENCE public.order_money_changes_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

-- sequence owned by order_money_changes_id_seq
ALTER SEQUENCE public.order_money_changes_id_seq OWNED BY public.order_money_changes.id;

-- table order_placements
CREATE TABLE public.order_placements (
    channel_id uuid NOT NULL,
    client_order_id text NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    body_hash text NOT NULL,
    request jsonb NOT NULL,
    card_token text DEFAULT ''::text NOT NULL,
    verification_token text DEFAULT ''::text NOT NULL,
    customer_id uuid,
    operator jsonb,
    expires_at timestamp with time zone,
    state text DEFAULT 'pending'::text NOT NULL,
    waiting text DEFAULT ''::text NOT NULL,
    result jsonb,
    order_id uuid,
    worker text DEFAULT ''::text NOT NULL,
    attempts integer DEFAULT 1 NOT NULL,
    charge_started boolean DEFAULT false NOT NULL,
    charge_cents bigint DEFAULT 0 NOT NULL,
    due_alert_at timestamp with time zone,
    due_alerted boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT order_placements_state_check CHECK ((state = ANY (ARRAY['pending'::text, 'placed'::text, 'refused'::text, 'expired'::text, 'voided'::text]))),
    CONSTRAINT order_placements_waiting_check CHECK ((waiting = ANY (ARRAY[''::text, 'verification'::text, 'retry'::text])))
);

-- table orders
CREATE TABLE public.orders (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    schema_version integer DEFAULT 1 NOT NULL,
    source text NOT NULL,
    source_event_id text NOT NULL,
    kitchen_order_number integer,
    status text NOT NULL,
    source_status text DEFAULT ''::text NOT NULL,
    currency text NOT NULL,
    subtotal_cents bigint NOT NULL,
    tax_cents bigint NOT NULL,
    tip_cents bigint NOT NULL,
    total_cents bigint NOT NULL,
    fees jsonb DEFAULT '[]'::jsonb NOT NULL,
    discounts jsonb DEFAULT '[]'::jsonb NOT NULL,
    customer_display_name text DEFAULT ''::text NOT NULL,
    customer_email text DEFAULT ''::text NOT NULL,
    customer_phone text DEFAULT ''::text NOT NULL,
    customer_external_id text DEFAULT ''::text NOT NULL,
    fulfillment_type text NOT NULL,
    fulfillment_address jsonb,
    promised_at timestamp with time zone,
    table_id text DEFAULT ''::text NOT NULL,
    notes text DEFAULT ''::text NOT NULL,
    source_metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    raw_payload bytea NOT NULL,
    placed_at timestamp with time zone NOT NULL,
    received_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    estimated_ready_at timestamp with time zone,
    driver_name text DEFAULT ''::text NOT NULL,
    floor_at timestamp with time zone,
    agent_ack_at timestamp with time zone,
    agent_status jsonb,
    agent_status_at timestamp with time zone,
    agent_batch_id bigint,
    delivery_fee_cents bigint GENERATED ALWAYS AS (public.money_fee_cents(fees, 'delivery'::text, 'Amount'::text)) STORED,
    driver_fee_cents bigint GENERATED ALWAYS AS (public.money_fee_cents(fees, 'delivery'::text, 'DriverCents'::text)) STORED,
    service_fee_cents bigint GENERATED ALWAYS AS (public.money_fee_cents(fees, 'service'::text, 'Amount'::text)) STORED,
    card_fee_cents bigint GENERATED ALWAYS AS (public.money_fee_cents(fees, 'card'::text, 'Amount'::text)) STORED,
    discount_cents bigint GENERATED ALWAYS AS (public.money_fee_cents(discounts, ''::text, 'Amount'::text)) STORED,
    money_changed_at timestamp with time zone,
    CONSTRAINT orders_fulfillment_ck CHECK ((fulfillment_type = ANY (ARRAY['pickup'::text, 'delivery'::text, 'dine_in'::text, 'counter'::text]))),
    CONSTRAINT orders_money_ck CHECK (((subtotal_cents >= 0) AND (tax_cents >= 0) AND (tip_cents >= 0) AND (total_cents >= 0))),
    CONSTRAINT orders_source_ck CHECK ((source = ANY (ARRAY['online'::text, 'mobile'::text, 'pos'::text, 'kiosk'::text, 'counter'::text, 'web'::text, 'operator'::text, 'vendor'::text]))),
    CONSTRAINT orders_status_ck CHECK ((status = ANY (ARRAY['received'::text, 'validated'::text, 'scheduled'::text, 'accepted'::text, 'preparing'::text, 'ready'::text, 'on_delivery'::text, 'completed'::text, 'rejected'::text, 'cancelled'::text])))
);

-- table outbox_dispatches
CREATE TABLE public.outbox_dispatches (
    event_id uuid NOT NULL,
    handler_name text NOT NULL,
    dispatched_at timestamp with time zone,
    attempts integer DEFAULT 0 NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    last_attempt_at timestamp with time zone,
    next_attempt_at timestamp with time zone
);

-- table outbox_events
CREATE TABLE public.outbox_events (
    id uuid NOT NULL,
    aggregate_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    type text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    occurred_at timestamp with time zone NOT NULL,
    schema_version integer DEFAULT 1 NOT NULL
);

-- table payments
CREATE TABLE public.payments (
    id uuid NOT NULL,
    order_id uuid,
    processor text NOT NULL,
    status text NOT NULL,
    amount_cents bigint NOT NULL,
    token text DEFAULT ''::text NOT NULL,
    last4 text DEFAULT ''::text NOT NULL,
    captured_at timestamp with time zone,
    auth_code text DEFAULT ''::text NOT NULL,
    tip_cents bigint DEFAULT 0 NOT NULL,
    staff_id uuid,
    refund_of uuid,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    location_id uuid NOT NULL,
    client_key text DEFAULT ''::text NOT NULL,
    origin text DEFAULT ''::text NOT NULL,
    batch_id bigint,
    approved_by text DEFAULT ''::text NOT NULL,
    flags text[] DEFAULT '{}'::text[] NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    CONSTRAINT payments_refund_sign_ck CHECK (((status = 'refunded'::text) = (amount_cents < 0))),
    CONSTRAINT payments_status_ck CHECK ((status = ANY (ARRAY['pending'::text, 'authorized'::text, 'captured'::text, 'failed'::text, 'refunded'::text, 'voided'::text]))),
    CONSTRAINT payments_tip_ck CHECK ((tip_cents >= 0)),
    CONSTRAINT payments_unlinked_ck CHECK (((order_id IS NOT NULL) OR ((origin = 'counter'::text) AND (status = 'refunded'::text) AND (location_id IS NOT NULL))))
);

-- table platform_keys
CREATE TABLE public.platform_keys (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    key_hash text NOT NULL,
    key_prefix text NOT NULL,
    revoked_at timestamp with time zone,
    last_used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table pricing_settings
CREATE TABLE public.pricing_settings (
    location_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    kind text DEFAULT ''::text NOT NULL,
    settings jsonb DEFAULT '{}'::jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT pricing_settings_kind_check CHECK ((kind = ANY (ARRAY[''::text, 'web'::text, 'operator'::text, 'counter'::text, 'vendor'::text])))
);

-- table printer_ip_changes
CREATE TABLE public.printer_ip_changes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    printer_id uuid NOT NULL,
    old_ip text DEFAULT ''::text NOT NULL,
    new_ip text DEFAULT ''::text NOT NULL,
    mac text DEFAULT ''::text NOT NULL,
    source text NOT NULL,
    at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT printer_ip_changes_source_check CHECK ((source = ANY (ARRAY['agent'::text, 'manager'::text])))
);

-- table printers
CREATE TABLE public.printers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    name text NOT NULL,
    ip text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    is_receipt boolean DEFAULT false NOT NULL,
    mac text DEFAULT ''::text NOT NULL
);

-- table schema_baseline
CREATE TABLE public.schema_baseline (
    id smallint DEFAULT 1 NOT NULL,
    baseline text NOT NULL,
    squashed_from integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT schema_baseline_id_check CHECK ((id = 1))
);

-- table site_domains
CREATE TABLE public.site_domains (
    host text NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    is_primary boolean DEFAULT false NOT NULL,
    verified boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table site_images
CREATE TABLE public.site_images (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    location_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    kind text NOT NULL,
    content_type text NOT NULL,
    bytes bytea NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT site_images_kind_check CHECK ((kind = ANY (ARRAY['logo'::text, 'cover'::text])))
);

-- table site_settings
CREATE TABLE public.site_settings (
    location_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    draft jsonb DEFAULT '{}'::jsonb NOT NULL,
    published jsonb DEFAULT '{}'::jsonb NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    reply_email text DEFAULT ''::text NOT NULL,
    draft_at timestamp with time zone,
    published_at timestamp with time zone,
    published_by text DEFAULT ''::text NOT NULL,
    preview_token text DEFAULT ''::text NOT NULL
);

-- table square_connections
CREATE TABLE public.square_connections (
    location_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    merchant_id text NOT NULL,
    square_location_id text DEFAULT ''::text NOT NULL,
    access_token_enc bytea NOT NULL,
    refresh_token_enc bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    refreshed_at timestamp with time zone DEFAULT now() NOT NULL,
    status text DEFAULT 'connected'::text NOT NULL,
    last_error text DEFAULT ''::text NOT NULL,
    connected_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT square_connections_status_check CHECK ((status = ANY (ARRAY['connected'::text, 'revoked'::text, 'error'::text])))
);

-- table staff
CREATE TABLE public.staff (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    first_name text NOT NULL,
    last_name text DEFAULT ''::text NOT NULL,
    pin_hash text NOT NULL,
    role text NOT NULL,
    limits jsonb DEFAULT '{}'::jsonb NOT NULL,
    active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT staff_role_check CHECK ((role = ANY (ARRAY['cashier'::text, 'manager'::text, 'driver'::text])))
);

-- table staff_journal
CREATE TABLE public.staff_journal (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    at timestamp with time zone NOT NULL,
    staff_id uuid,
    device_id text DEFAULT ''::text NOT NULL,
    action text NOT NULL,
    order_id uuid,
    detail jsonb DEFAULT '{}'::jsonb NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table status_events
CREATE TABLE public.status_events (
    order_id uuid NOT NULL,
    "position" integer NOT NULL,
    from_status text DEFAULT ''::text NOT NULL,
    to_status text NOT NULL,
    at timestamp with time zone NOT NULL,
    actor text NOT NULL,
    actor_id uuid,
    reason text DEFAULT ''::text NOT NULL
);

-- table store_hours_exceptions
CREATE TABLE public.store_hours_exceptions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    day date NOT NULL,
    closed boolean NOT NULL,
    hours jsonb DEFAULT '[]'::jsonb NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table store_ordering
CREATE TABLE public.store_ordering (
    location_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    pickup_enabled boolean DEFAULT true NOT NULL,
    delivery_enabled boolean DEFAULT true NOT NULL,
    pickup_eta_minutes integer DEFAULT 20 NOT NULL,
    delivery_eta_minutes integer DEFAULT 45 NOT NULL,
    schedule_days integer DEFAULT 7 NOT NULL,
    paused_at timestamp with time zone,
    paused_until timestamp with time zone,
    pause_note text DEFAULT ''::text NOT NULL,
    restocked_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    tip_max_cents bigint DEFAULT 5000 NOT NULL,
    CONSTRAINT store_ordering_delivery_eta_minutes_check CHECK (((delivery_eta_minutes >= 0) AND (delivery_eta_minutes <= 240))),
    CONSTRAINT store_ordering_pickup_eta_minutes_check CHECK (((pickup_eta_minutes >= 0) AND (pickup_eta_minutes <= 240))),
    CONSTRAINT store_ordering_schedule_days_check CHECK (((schedule_days >= 0) AND (schedule_days <= 30))),
    CONSTRAINT store_ordering_tip_max_cents_check CHECK (((tip_max_cents >= 0) AND (tip_max_cents <= 100000)))
);

-- table tablet_report_tokens
CREATE TABLE public.tablet_report_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    tablet_id text NOT NULL,
    tablet_name text DEFAULT ''::text NOT NULL,
    token_hash text NOT NULL,
    key_prefix text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone,
    revoked_at timestamp with time zone
);

-- table tenants
CREATE TABLE public.tenants (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    slug text NOT NULL,
    name text NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    username text,
    password_hash text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    store_id bigint,
    test_store boolean DEFAULT false NOT NULL,
    retired_at timestamp with time zone
);

-- table terminal_settings
CREATE TABLE public.terminal_settings (
    location_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    settings jsonb DEFAULT '{}'::jsonb NOT NULL,
    version bigint DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

-- table z_report_versions
CREATE TABLE public.z_report_versions (
    seq bigint NOT NULL,
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    batch_id bigint NOT NULL,
    version integer NOT NULL,
    business_date date NOT NULL,
    opened_at timestamp with time zone,
    closed_at timestamp with time zone,
    report jsonb NOT NULL,
    received_at timestamp with time zone NOT NULL,
    superseded_at timestamp with time zone DEFAULT now() NOT NULL,
    superseded_by uuid NOT NULL
);

-- sequence z_report_versions_seq_seq
CREATE SEQUENCE public.z_report_versions_seq_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

-- sequence owned by z_report_versions_seq_seq
ALTER SEQUENCE public.z_report_versions_seq_seq OWNED BY public.z_report_versions.seq;

-- table z_reports
CREATE TABLE public.z_reports (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    batch_id bigint NOT NULL,
    business_date date NOT NULL,
    opened_at timestamp with time zone,
    closed_at timestamp with time zone,
    report jsonb NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    flagged boolean DEFAULT false NOT NULL,
    main_check jsonb,
    main_difference_cents bigint,
    sales_total_cents bigint GENERATED ALWAYS AS (public.money_cents((report #>> '{sales,total_cents}'::text[]))) STORED,
    sales_tax_cents bigint GENERATED ALWAYS AS (public.money_cents((report #>> '{sales,tax_cents}'::text[]))) STORED,
    tips_card_cents bigint GENERATED ALWAYS AS (public.money_cents((report #>> '{tips,card_cents}'::text[]))) STORED,
    tips_cash_cents bigint GENERATED ALWAYS AS (public.money_cents((report #>> '{tips,cash_cents}'::text[]))) STORED,
    refunds_cents bigint GENERATED ALWAYS AS (public.money_cents((report #>> '{refunds,cents}'::text[]))) STORED,
    over_short_cents bigint GENERATED ALWAYS AS (public.money_cents((report #>> '{over_short_cents}'::text[]))) STORED,
    difference_cents bigint GENERATED ALWAYS AS (public.money_cents((report #>> '{reconcile,difference_cents}'::text[]))) STORED
);

-- default channel_events id
ALTER TABLE ONLY public.channel_events ALTER COLUMN id SET DEFAULT nextval('public.channel_events_id_seq'::regclass);

-- default drawer_session_revisions seq
ALTER TABLE ONLY public.drawer_session_revisions ALTER COLUMN seq SET DEFAULT nextval('public.drawer_session_revisions_seq_seq'::regclass);

-- default money_flags id
ALTER TABLE ONLY public.money_flags ALTER COLUMN id SET DEFAULT nextval('public.money_flags_id_seq'::regclass);

-- default order_money_changes id
ALTER TABLE ONLY public.order_money_changes ALTER COLUMN id SET DEFAULT nextval('public.order_money_changes_id_seq'::regclass);

-- default z_report_versions seq
ALTER TABLE ONLY public.z_report_versions ALTER COLUMN seq SET DEFAULT nextval('public.z_report_versions_seq_seq'::regclass);

-- constraint agent_backups agent_backups_pkey
ALTER TABLE ONLY public.agent_backups
    ADD CONSTRAINT agent_backups_pkey PRIMARY KEY (id);

-- constraint agent_health agent_health_pkey
ALTER TABLE ONLY public.agent_health
    ADD CONSTRAINT agent_health_pkey PRIMARY KEY (location_id);

-- constraint agent_state agent_state_pkey
ALTER TABLE ONLY public.agent_state
    ADD CONSTRAINT agent_state_pkey PRIMARY KEY (location_id);

-- constraint agent_updates agent_updates_pkey
ALTER TABLE ONLY public.agent_updates
    ADD CONSTRAINT agent_updates_pkey PRIMARY KEY (id);

-- constraint app_releases app_releases_app_version_code_key
ALTER TABLE ONLY public.app_releases
    ADD CONSTRAINT app_releases_app_version_code_key UNIQUE (app, version_code);

-- constraint app_releases app_releases_pkey
ALTER TABLE ONLY public.app_releases
    ADD CONSTRAINT app_releases_pkey PRIMARY KEY (id);

-- constraint audit_log audit_log_pkey
ALTER TABLE ONLY public.audit_log
    ADD CONSTRAINT audit_log_pkey PRIMARY KEY (id);

-- constraint card_holds card_holds_pkey
ALTER TABLE ONLY public.card_holds
    ADD CONSTRAINT card_holds_pkey PRIMARY KEY (order_id);

-- constraint channel_events channel_events_pkey
ALTER TABLE ONLY public.channel_events
    ADD CONSTRAINT channel_events_pkey PRIMARY KEY (id);

-- constraint channel_orders channel_orders_pkey
ALTER TABLE ONLY public.channel_orders
    ADD CONSTRAINT channel_orders_pkey PRIMARY KEY (order_id);

-- constraint channels channels_key_hash_key
ALTER TABLE ONLY public.channels
    ADD CONSTRAINT channels_key_hash_key UNIQUE (key_hash);

-- constraint channels channels_pkey
ALTER TABLE ONLY public.channels
    ADD CONSTRAINT channels_pkey PRIMARY KEY (id);

-- constraint customer_addresses customer_addresses_pkey
ALTER TABLE ONLY public.customer_addresses
    ADD CONSTRAINT customer_addresses_pkey PRIMARY KEY (id);

-- constraint customer_cards customer_cards_customer_id_card_ref_key
ALTER TABLE ONLY public.customer_cards
    ADD CONSTRAINT customer_cards_customer_id_card_ref_key UNIQUE (customer_id, card_ref);

-- constraint customer_cards customer_cards_pkey
ALTER TABLE ONLY public.customer_cards
    ADD CONSTRAINT customer_cards_pkey PRIMARY KEY (id);

-- constraint customer_sessions customer_sessions_pkey
ALTER TABLE ONLY public.customer_sessions
    ADD CONSTRAINT customer_sessions_pkey PRIMARY KEY (token_hash);

-- constraint customer_tombstones customer_tombstones_pkey
ALTER TABLE ONLY public.customer_tombstones
    ADD CONSTRAINT customer_tombstones_pkey PRIMARY KEY (customer_id);

-- constraint customers customers_location_id_phone_key
ALTER TABLE ONLY public.customers
    ADD CONSTRAINT customers_location_id_phone_key UNIQUE (location_id, phone);

-- constraint customers customers_pkey
ALTER TABLE ONLY public.customers
    ADD CONSTRAINT customers_pkey PRIMARY KEY (id);

-- constraint deals deals_pkey
ALTER TABLE ONLY public.deals
    ADD CONSTRAINT deals_pkey PRIMARY KEY (id);

-- constraint delivery_runs delivery_runs_pkey
ALTER TABLE ONLY public.delivery_runs
    ADD CONSTRAINT delivery_runs_pkey PRIMARY KEY (id);

-- constraint device_tombstones device_tombstones_pkey
ALTER TABLE ONLY public.device_tombstones
    ADD CONSTRAINT device_tombstones_pkey PRIMARY KEY (key_hash);

-- constraint devices devices_key_hash_key
ALTER TABLE ONLY public.devices
    ADD CONSTRAINT devices_key_hash_key UNIQUE (key_hash);

-- constraint devices devices_pkey
ALTER TABLE ONLY public.devices
    ADD CONSTRAINT devices_pkey PRIMARY KEY (id);

-- constraint diag_reports diag_reports_pkey
ALTER TABLE ONLY public.diag_reports
    ADD CONSTRAINT diag_reports_pkey PRIMARY KEY (id);

-- constraint drawer_movements drawer_movements_pkey
ALTER TABLE ONLY public.drawer_movements
    ADD CONSTRAINT drawer_movements_pkey PRIMARY KEY (id);

-- constraint drawer_session_revisions drawer_session_revisions_pkey
ALTER TABLE ONLY public.drawer_session_revisions
    ADD CONSTRAINT drawer_session_revisions_pkey PRIMARY KEY (seq);

-- constraint drawer_sessions drawer_sessions_pkey
ALTER TABLE ONLY public.drawer_sessions
    ADD CONSTRAINT drawer_sessions_pkey PRIMARY KEY (id);

-- constraint driver_shifts driver_shifts_pkey
ALTER TABLE ONLY public.driver_shifts
    ADD CONSTRAINT driver_shifts_pkey PRIMARY KEY (location_id, id);

-- constraint drivers drivers_pkey
ALTER TABLE ONLY public.drivers
    ADD CONSTRAINT drivers_pkey PRIMARY KEY (location_id, id);

-- constraint email_receipts email_receipts_pkey
ALTER TABLE ONLY public.email_receipts
    ADD CONSTRAINT email_receipts_pkey PRIMARY KEY (order_id, kind);

-- constraint floor_events_seen floor_events_seen_pkey
ALTER TABLE ONLY public.floor_events_seen
    ADD CONSTRAINT floor_events_seen_pkey PRIMARY KEY (event_id);

-- constraint geo_cache geo_cache_pkey
ALTER TABLE ONLY public.geo_cache
    ADD CONSTRAINT geo_cache_pkey PRIMARY KEY (address_key);

-- constraint item_modifier_groups item_modifier_groups_pkey
ALTER TABLE ONLY public.item_modifier_groups
    ADD CONSTRAINT item_modifier_groups_pkey PRIMARY KEY (item_id, group_id);

-- constraint line_items line_items_pkey
ALTER TABLE ONLY public.line_items
    ADD CONSTRAINT line_items_pkey PRIMARY KEY (id);

-- constraint locations locations_id_tenant_id_key
ALTER TABLE ONLY public.locations
    ADD CONSTRAINT locations_id_tenant_id_key UNIQUE (id, tenant_id);

-- constraint locations locations_pkey
ALTER TABLE ONLY public.locations
    ADD CONSTRAINT locations_pkey PRIMARY KEY (id);

-- constraint locations locations_tenant_id_slug_key
ALTER TABLE ONLY public.locations
    ADD CONSTRAINT locations_tenant_id_slug_key UNIQUE (tenant_id, slug);

-- constraint menu_categories menu_categories_id_tenant_id_key
ALTER TABLE ONLY public.menu_categories
    ADD CONSTRAINT menu_categories_id_tenant_id_key UNIQUE (id, tenant_id);

-- constraint menu_categories menu_categories_pkey
ALTER TABLE ONLY public.menu_categories
    ADD CONSTRAINT menu_categories_pkey PRIMARY KEY (id);

-- constraint menu_families menu_families_pkey
ALTER TABLE ONLY public.menu_families
    ADD CONSTRAINT menu_families_pkey PRIMARY KEY (id);

-- constraint menu_item_sizes menu_item_sizes_pkey
ALTER TABLE ONLY public.menu_item_sizes
    ADD CONSTRAINT menu_item_sizes_pkey PRIMARY KEY (item_id, size_id);

-- constraint menu_items menu_items_id_tenant_id_key
ALTER TABLE ONLY public.menu_items
    ADD CONSTRAINT menu_items_id_tenant_id_key UNIQUE (id, tenant_id);

-- constraint menu_items menu_items_pkey
ALTER TABLE ONLY public.menu_items
    ADD CONSTRAINT menu_items_pkey PRIMARY KEY (id);

-- constraint menu_sizes menu_sizes_id_tenant_id_key
ALTER TABLE ONLY public.menu_sizes
    ADD CONSTRAINT menu_sizes_id_tenant_id_key UNIQUE (id, tenant_id);

-- constraint menu_sizes menu_sizes_pkey
ALTER TABLE ONLY public.menu_sizes
    ADD CONSTRAINT menu_sizes_pkey PRIMARY KEY (id);

-- constraint menu_versions menu_versions_pkey
ALTER TABLE ONLY public.menu_versions
    ADD CONSTRAINT menu_versions_pkey PRIMARY KEY (location_id);

-- constraint menu_view_builds menu_view_builds_pkey
ALTER TABLE ONLY public.menu_view_builds
    ADD CONSTRAINT menu_view_builds_pkey PRIMARY KEY (view_id);

-- constraint menu_view_entries menu_view_entries_pkey
ALTER TABLE ONLY public.menu_view_entries
    ADD CONSTRAINT menu_view_entries_pkey PRIMARY KEY (id);

-- constraint menu_views menu_views_id_tenant_id_key
ALTER TABLE ONLY public.menu_views
    ADD CONSTRAINT menu_views_id_tenant_id_key UNIQUE (id, tenant_id);

-- constraint menu_views menu_views_pkey
ALTER TABLE ONLY public.menu_views
    ADD CONSTRAINT menu_views_pkey PRIMARY KEY (id);

-- constraint menus menus_id_tenant_id_key
ALTER TABLE ONLY public.menus
    ADD CONSTRAINT menus_id_tenant_id_key UNIQUE (id, tenant_id);

-- constraint menus menus_pkey
ALTER TABLE ONLY public.menus
    ADD CONSTRAINT menus_pkey PRIMARY KEY (id);

-- constraint modifier_groups modifier_groups_id_tenant_id_key
ALTER TABLE ONLY public.modifier_groups
    ADD CONSTRAINT modifier_groups_id_tenant_id_key UNIQUE (id, tenant_id);

-- constraint modifier_groups modifier_groups_pkey
ALTER TABLE ONLY public.modifier_groups
    ADD CONSTRAINT modifier_groups_pkey PRIMARY KEY (id);

-- constraint modifier_option_prices modifier_option_prices_pkey
ALTER TABLE ONLY public.modifier_option_prices
    ADD CONSTRAINT modifier_option_prices_pkey PRIMARY KEY (option_id, size_id);

-- constraint modifier_options modifier_options_id_tenant_id_key
ALTER TABLE ONLY public.modifier_options
    ADD CONSTRAINT modifier_options_id_tenant_id_key UNIQUE (id, tenant_id);

-- constraint modifier_options modifier_options_pkey
ALTER TABLE ONLY public.modifier_options
    ADD CONSTRAINT modifier_options_pkey PRIMARY KEY (id);

-- constraint money_flags money_flags_pkey
ALTER TABLE ONLY public.money_flags
    ADD CONSTRAINT money_flags_pkey PRIMARY KEY (id);

-- constraint operator_locations operator_locations_pkey
ALTER TABLE ONLY public.operator_locations
    ADD CONSTRAINT operator_locations_pkey PRIMARY KEY (operator_id, location_id);

-- constraint operator_tokens operator_tokens_pkey
ALTER TABLE ONLY public.operator_tokens
    ADD CONSTRAINT operator_tokens_pkey PRIMARY KEY (id);

-- constraint operator_tokens operator_tokens_token_hash_key
ALTER TABLE ONLY public.operator_tokens
    ADD CONSTRAINT operator_tokens_token_hash_key UNIQUE (token_hash);

-- constraint operators operators_pkey
ALTER TABLE ONLY public.operators
    ADD CONSTRAINT operators_pkey PRIMARY KEY (id);

-- constraint operators operators_username_key
ALTER TABLE ONLY public.operators
    ADD CONSTRAINT operators_username_key UNIQUE (username);

-- constraint order_geo order_geo_pkey
ALTER TABLE ONLY public.order_geo
    ADD CONSTRAINT order_geo_pkey PRIMARY KEY (order_id);

-- constraint order_money_changes order_money_changes_pkey
ALTER TABLE ONLY public.order_money_changes
    ADD CONSTRAINT order_money_changes_pkey PRIMARY KEY (id);

-- constraint order_placements order_placements_pkey
ALTER TABLE ONLY public.order_placements
    ADD CONSTRAINT order_placements_pkey PRIMARY KEY (channel_id, client_order_id);

-- constraint orders orders_pkey
ALTER TABLE ONLY public.orders
    ADD CONSTRAINT orders_pkey PRIMARY KEY (id);

-- constraint orders orders_tenant_id_source_source_event_id_key
ALTER TABLE ONLY public.orders
    ADD CONSTRAINT orders_tenant_id_source_source_event_id_key UNIQUE (tenant_id, source, source_event_id);

-- constraint outbox_dispatches outbox_dispatches_pkey
ALTER TABLE ONLY public.outbox_dispatches
    ADD CONSTRAINT outbox_dispatches_pkey PRIMARY KEY (event_id, handler_name);

-- constraint outbox_events outbox_events_pkey
ALTER TABLE ONLY public.outbox_events
    ADD CONSTRAINT outbox_events_pkey PRIMARY KEY (id);

-- constraint payments payments_pkey
ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_pkey PRIMARY KEY (id);

-- constraint platform_keys platform_keys_key_hash_key
ALTER TABLE ONLY public.platform_keys
    ADD CONSTRAINT platform_keys_key_hash_key UNIQUE (key_hash);

-- constraint platform_keys platform_keys_pkey
ALTER TABLE ONLY public.platform_keys
    ADD CONSTRAINT platform_keys_pkey PRIMARY KEY (id);

-- constraint pricing_settings pricing_settings_pkey
ALTER TABLE ONLY public.pricing_settings
    ADD CONSTRAINT pricing_settings_pkey PRIMARY KEY (location_id, kind);

-- constraint printer_ip_changes printer_ip_changes_pkey
ALTER TABLE ONLY public.printer_ip_changes
    ADD CONSTRAINT printer_ip_changes_pkey PRIMARY KEY (id);

-- constraint printers printers_location_id_name_key
ALTER TABLE ONLY public.printers
    ADD CONSTRAINT printers_location_id_name_key UNIQUE (location_id, name);

-- constraint printers printers_pkey
ALTER TABLE ONLY public.printers
    ADD CONSTRAINT printers_pkey PRIMARY KEY (id);

-- constraint schema_baseline schema_baseline_pkey
ALTER TABLE ONLY public.schema_baseline
    ADD CONSTRAINT schema_baseline_pkey PRIMARY KEY (id);

-- constraint site_domains site_domains_pkey
ALTER TABLE ONLY public.site_domains
    ADD CONSTRAINT site_domains_pkey PRIMARY KEY (host);

-- constraint site_images site_images_pkey
ALTER TABLE ONLY public.site_images
    ADD CONSTRAINT site_images_pkey PRIMARY KEY (id);

-- constraint site_settings site_settings_pkey
ALTER TABLE ONLY public.site_settings
    ADD CONSTRAINT site_settings_pkey PRIMARY KEY (location_id);

-- constraint square_connections square_connections_pkey
ALTER TABLE ONLY public.square_connections
    ADD CONSTRAINT square_connections_pkey PRIMARY KEY (location_id);

-- constraint staff_journal staff_journal_pkey
ALTER TABLE ONLY public.staff_journal
    ADD CONSTRAINT staff_journal_pkey PRIMARY KEY (id);

-- constraint staff staff_pkey
ALTER TABLE ONLY public.staff
    ADD CONSTRAINT staff_pkey PRIMARY KEY (id);

-- constraint status_events status_events_pkey
ALTER TABLE ONLY public.status_events
    ADD CONSTRAINT status_events_pkey PRIMARY KEY (order_id, "position");

-- constraint store_hours_exceptions store_hours_exceptions_location_id_day_key
ALTER TABLE ONLY public.store_hours_exceptions
    ADD CONSTRAINT store_hours_exceptions_location_id_day_key UNIQUE (location_id, day);

-- constraint store_hours_exceptions store_hours_exceptions_pkey
ALTER TABLE ONLY public.store_hours_exceptions
    ADD CONSTRAINT store_hours_exceptions_pkey PRIMARY KEY (id);

-- constraint store_ordering store_ordering_pkey
ALTER TABLE ONLY public.store_ordering
    ADD CONSTRAINT store_ordering_pkey PRIMARY KEY (location_id);

-- constraint tablet_report_tokens tablet_report_tokens_pkey
ALTER TABLE ONLY public.tablet_report_tokens
    ADD CONSTRAINT tablet_report_tokens_pkey PRIMARY KEY (id);

-- constraint tablet_report_tokens tablet_report_tokens_token_hash_key
ALTER TABLE ONLY public.tablet_report_tokens
    ADD CONSTRAINT tablet_report_tokens_token_hash_key UNIQUE (token_hash);

-- constraint tenants tenants_pkey
ALTER TABLE ONLY public.tenants
    ADD CONSTRAINT tenants_pkey PRIMARY KEY (id);

-- constraint tenants tenants_slug_key
ALTER TABLE ONLY public.tenants
    ADD CONSTRAINT tenants_slug_key UNIQUE (slug);

-- constraint terminal_settings terminal_settings_pkey
ALTER TABLE ONLY public.terminal_settings
    ADD CONSTRAINT terminal_settings_pkey PRIMARY KEY (location_id);

-- constraint z_report_versions z_report_versions_pkey
ALTER TABLE ONLY public.z_report_versions
    ADD CONSTRAINT z_report_versions_pkey PRIMARY KEY (seq);

-- constraint z_reports z_reports_id_key
ALTER TABLE ONLY public.z_reports
    ADD CONSTRAINT z_reports_id_key UNIQUE (id);

-- constraint z_reports z_reports_pkey
ALTER TABLE ONLY public.z_reports
    ADD CONSTRAINT z_reports_pkey PRIMARY KEY (location_id, batch_id);

-- index card_holds_ref_uq
CREATE UNIQUE INDEX card_holds_ref_uq ON public.card_holds USING btree (gateway, payment_ref);

-- index idx_agent_backups_location
CREATE INDEX idx_agent_backups_location ON public.agent_backups USING btree (location_id, created_at DESC);

-- index idx_agent_updates_location
CREATE INDEX idx_agent_updates_location ON public.agent_updates USING btree (location_id, requested_at DESC);

-- index idx_agent_updates_requested
CREATE INDEX idx_agent_updates_requested ON public.agent_updates USING btree (requested_at DESC);

-- index idx_audit_log_at
CREATE INDEX idx_audit_log_at ON public.audit_log USING brin (at);

-- index idx_audit_log_entity
CREATE INDEX idx_audit_log_entity ON public.audit_log USING btree (entity, entity_id);

-- index idx_audit_log_location
CREATE INDEX idx_audit_log_location ON public.audit_log USING btree (location_id, id DESC);

-- index idx_audit_log_trace
CREATE INDEX idx_audit_log_trace ON public.audit_log USING btree (trace_id) WHERE (trace_id <> ''::text);

-- index idx_card_holds_location
CREATE INDEX idx_card_holds_location ON public.card_holds USING btree (location_id, created_at DESC);

-- index idx_card_holds_payment
CREATE INDEX idx_card_holds_payment ON public.card_holds USING btree (payment_id) WHERE (payment_id IS NOT NULL);

-- index idx_card_holds_ref
CREATE INDEX idx_card_holds_ref ON public.card_holds USING btree (location_id, payment_ref);

-- index idx_card_holds_tip_payment
CREATE INDEX idx_card_holds_tip_payment ON public.card_holds USING btree (tip_payment_id) WHERE (tip_payment_id IS NOT NULL);

-- index idx_card_holds_undo
CREATE INDEX idx_card_holds_undo ON public.card_holds USING btree (next_try_at) WHERE ((cancel_requested_at IS NOT NULL) OR (refund_requested_at IS NOT NULL));

-- index idx_card_holds_work
CREATE INDEX idx_card_holds_work ON public.card_holds USING btree (next_try_at) WHERE (status = 'held'::text);

-- index idx_channel_events_channel
CREATE INDEX idx_channel_events_channel ON public.channel_events USING btree (channel_id, id);

-- index idx_channel_events_created
CREATE INDEX idx_channel_events_created ON public.channel_events USING btree (created_at);

-- index idx_channel_orders_channel
CREATE INDEX idx_channel_orders_channel ON public.channel_orders USING btree (channel_id, created_at DESC);

-- index idx_channel_orders_client
CREATE UNIQUE INDEX idx_channel_orders_client ON public.channel_orders USING btree (channel_id, client_order_id) WHERE (client_order_id <> ''::text);

-- index idx_channel_orders_customer
CREATE INDEX idx_channel_orders_customer ON public.channel_orders USING btree (customer_id, created_at DESC) WHERE (customer_id IS NOT NULL);

-- index idx_channel_orders_operator
CREATE INDEX idx_channel_orders_operator ON public.channel_orders USING btree (operator_id) WHERE (operator_id IS NOT NULL);

-- index idx_channel_orders_track
CREATE UNIQUE INDEX idx_channel_orders_track ON public.channel_orders USING btree (track_token) WHERE (track_token IS NOT NULL);

-- index idx_channels_location
CREATE INDEX idx_channels_location ON public.channels USING btree (location_id);

-- index idx_channels_view
CREATE INDEX idx_channels_view ON public.channels USING btree (view_id) WHERE (view_id IS NOT NULL);

-- index idx_customer_addresses_customer
CREATE INDEX idx_customer_addresses_customer ON public.customer_addresses USING btree (customer_id);

-- index idx_customer_addresses_default
CREATE UNIQUE INDEX idx_customer_addresses_default ON public.customer_addresses USING btree (customer_id) WHERE is_default;

-- index idx_customer_sessions_customer
CREATE INDEX idx_customer_sessions_customer ON public.customer_sessions USING btree (customer_id);

-- index idx_customer_sessions_expires
CREATE INDEX idx_customer_sessions_expires ON public.customer_sessions USING btree (expires_at);

-- index idx_customer_tombstones_changes
CREATE INDEX idx_customer_tombstones_changes ON public.customer_tombstones USING btree (location_id, deleted_at, customer_id);

-- index idx_customers_changes
CREATE INDEX idx_customers_changes ON public.customers USING btree (location_id, updated_at, id);

-- index idx_deals_code
CREATE UNIQUE INDEX idx_deals_code ON public.deals USING btree (location_id, upper(code)) WHERE (kind = 'code'::text);

-- index idx_deals_location
CREATE INDEX idx_deals_location ON public.deals USING btree (location_id);

-- index idx_delivery_runs_location
CREATE INDEX idx_delivery_runs_location ON public.delivery_runs USING btree (location_id, at DESC);

-- index idx_devices_location
CREATE INDEX idx_devices_location ON public.devices USING btree (location_id);

-- index idx_diag_reports_location
CREATE INDEX idx_diag_reports_location ON public.diag_reports USING btree (location_id, received_at DESC);

-- index idx_diag_reports_received
CREATE INDEX idx_diag_reports_received ON public.diag_reports USING btree (received_at DESC);

-- index idx_drawer_movements_drawer
CREATE INDEX idx_drawer_movements_drawer ON public.drawer_movements USING btree (drawer_id, at);

-- index idx_drawer_movements_location
CREATE INDEX idx_drawer_movements_location ON public.drawer_movements USING btree (location_id, at);

-- index idx_drawer_session_revisions_drawer
CREATE INDEX idx_drawer_session_revisions_drawer ON public.drawer_session_revisions USING btree (drawer_id, seq);

-- index idx_drawer_session_revisions_location
CREATE INDEX idx_drawer_session_revisions_location ON public.drawer_session_revisions USING btree (location_id);

-- index idx_drawer_sessions_location
CREATE INDEX idx_drawer_sessions_location ON public.drawer_sessions USING btree (location_id, opened_at DESC);

-- index idx_driver_shifts_location
CREATE INDEX idx_driver_shifts_location ON public.driver_shifts USING btree (location_id, clock_in DESC);

-- index idx_drivers_location
CREATE INDEX idx_drivers_location ON public.drivers USING btree (location_id, "position");

-- index idx_email_receipts_work
CREATE INDEX idx_email_receipts_work ON public.email_receipts USING btree (next_try_at) WHERE (status = 'pending'::text);

-- index idx_floor_events_seen_location
CREATE INDEX idx_floor_events_seen_location ON public.floor_events_seen USING btree (location_id, received_at DESC);

-- index idx_item_modifier_groups_group
CREATE INDEX idx_item_modifier_groups_group ON public.item_modifier_groups USING btree (group_id);

-- index idx_line_items_order_id
CREATE INDEX idx_line_items_order_id ON public.line_items USING btree (order_id);

-- index idx_line_items_parent_id
CREATE INDEX idx_line_items_parent_id ON public.line_items USING btree (parent_id);

-- index idx_locations_one_per_tenant
CREATE UNIQUE INDEX idx_locations_one_per_tenant ON public.locations USING btree (tenant_id);

-- index idx_menu_categories_location
CREATE INDEX idx_menu_categories_location ON public.menu_categories USING btree (location_id);

-- index idx_menu_categories_menu
CREATE INDEX idx_menu_categories_menu ON public.menu_categories USING btree (menu_id);

-- index idx_menu_categories_printer
CREATE INDEX idx_menu_categories_printer ON public.menu_categories USING btree (default_printer_id) WHERE (default_printer_id IS NOT NULL);

-- index idx_menu_families_location
CREATE INDEX idx_menu_families_location ON public.menu_families USING btree (location_id);

-- index idx_menu_families_name
CREATE UNIQUE INDEX idx_menu_families_name ON public.menu_families USING btree (location_id, kind, lower(btrim(name)));

-- index idx_menu_item_sizes_size
CREATE INDEX idx_menu_item_sizes_size ON public.menu_item_sizes USING btree (size_id);

-- index idx_menu_items_category
CREATE INDEX idx_menu_items_category ON public.menu_items USING btree (category_id);

-- index idx_menu_items_family
CREATE INDEX idx_menu_items_family ON public.menu_items USING btree (family_id) WHERE (family_id IS NOT NULL);

-- index idx_menu_items_location
CREATE INDEX idx_menu_items_location ON public.menu_items USING btree (location_id);

-- index idx_menu_items_printer
CREATE INDEX idx_menu_items_printer ON public.menu_items USING btree (printer_id) WHERE (printer_id IS NOT NULL);

-- index idx_menu_sizes_name
CREATE UNIQUE INDEX idx_menu_sizes_name ON public.menu_sizes USING btree (location_id, lower(name));

-- index idx_menu_view_entries_view
CREATE INDEX idx_menu_view_entries_view ON public.menu_view_entries USING btree (view_id);

-- index idx_menu_views_location
CREATE INDEX idx_menu_views_location ON public.menu_views USING btree (location_id);

-- index idx_menus_location
CREATE INDEX idx_menus_location ON public.menus USING btree (location_id);

-- index idx_modifier_groups_location
CREATE INDEX idx_modifier_groups_location ON public.modifier_groups USING btree (location_id);

-- index idx_modifier_option_prices_size
CREATE INDEX idx_modifier_option_prices_size ON public.modifier_option_prices USING btree (size_id);

-- index idx_modifier_options_family
CREATE INDEX idx_modifier_options_family ON public.modifier_options USING btree (family_id) WHERE (family_id IS NOT NULL);

-- index idx_modifier_options_group
CREATE INDEX idx_modifier_options_group ON public.modifier_options USING btree (group_id);

-- index idx_money_flags_location
CREATE INDEX idx_money_flags_location ON public.money_flags USING btree (location_id, at DESC);

-- index idx_operator_locations_location
CREATE INDEX idx_operator_locations_location ON public.operator_locations USING btree (location_id);

-- index idx_operator_locations_op
CREATE INDEX idx_operator_locations_op ON public.operator_locations USING btree (operator_id);

-- index idx_operator_tokens_operator
CREATE INDEX idx_operator_tokens_operator ON public.operator_tokens USING btree (operator_id);

-- index idx_order_geo_due
CREATE INDEX idx_order_geo_due ON public.order_geo USING btree (next_try_at) WHERE (pushed_at IS NULL);

-- index idx_order_geo_location
CREATE INDEX idx_order_geo_location ON public.order_geo USING btree (location_id, state);

-- index idx_order_money_changes_order
CREATE INDEX idx_order_money_changes_order ON public.order_money_changes USING btree (order_id, id);

-- index idx_order_placements_age
CREATE INDEX idx_order_placements_age ON public.order_placements USING btree (created_at);

-- index idx_order_placements_due
CREATE INDEX idx_order_placements_due ON public.order_placements USING btree (due_alert_at) WHERE ((state = 'placed'::text) AND (NOT due_alerted));

-- index idx_order_placements_pending
CREATE INDEX idx_order_placements_pending ON public.order_placements USING btree (updated_at) WHERE (state = 'pending'::text);

-- index idx_orders_agent_batch
CREATE INDEX idx_orders_agent_batch ON public.orders USING btree (location_id, agent_batch_id);

-- index idx_orders_location_received
CREATE INDEX idx_orders_location_received ON public.orders USING btree (location_id, received_at DESC);

-- index idx_orders_phone
CREATE INDEX idx_orders_phone ON public.orders USING btree (location_id, customer_phone) WHERE (customer_phone <> ''::text);

-- index idx_orders_received_at
CREATE INDEX idx_orders_received_at ON public.orders USING btree (received_at DESC);

-- index idx_orders_source_event
CREATE INDEX idx_orders_source_event ON public.orders USING btree (source_event_id);

-- index idx_orders_unacked
CREATE INDEX idx_orders_unacked ON public.orders USING btree (received_at) WHERE (agent_ack_at IS NULL);

-- index idx_outbox_dispatches_pending
CREATE INDEX idx_outbox_dispatches_pending ON public.outbox_dispatches USING btree (handler_name, next_attempt_at NULLS FIRST) WHERE (dispatched_at IS NULL);

-- index idx_outbox_events_aggregate_id
CREATE INDEX idx_outbox_events_aggregate_id ON public.outbox_events USING btree (aggregate_id);

-- index idx_outbox_events_occurred_at
CREATE INDEX idx_outbox_events_occurred_at ON public.outbox_events USING btree (occurred_at);

-- index idx_outbox_events_scope
CREATE INDEX idx_outbox_events_scope ON public.outbox_events USING btree (tenant_id, location_id);

-- index idx_payments_counter_batch
CREATE INDEX idx_payments_counter_batch ON public.payments USING btree (location_id, batch_id) WHERE (origin = 'counter'::text);

-- index idx_payments_location_received
CREATE INDEX idx_payments_location_received ON public.payments USING btree (location_id, received_at);

-- index idx_payments_order_id
CREATE INDEX idx_payments_order_id ON public.payments USING btree (order_id);

-- index idx_payments_refund_of
CREATE INDEX idx_payments_refund_of ON public.payments USING btree (refund_of) WHERE (refund_of IS NOT NULL);

-- index idx_payments_unlinked
CREATE INDEX idx_payments_unlinked ON public.payments USING btree (location_id, batch_id) WHERE (order_id IS NULL);

-- index idx_printer_ip_changes_location
CREATE INDEX idx_printer_ip_changes_location ON public.printer_ip_changes USING btree (location_id, at DESC);

-- index idx_printer_ip_changes_printer
CREATE INDEX idx_printer_ip_changes_printer ON public.printer_ip_changes USING btree (printer_id);

-- index idx_printers_one_receipt
CREATE UNIQUE INDEX idx_printers_one_receipt ON public.printers USING btree (location_id) WHERE is_receipt;

-- index idx_site_domains_location
CREATE INDEX idx_site_domains_location ON public.site_domains USING btree (location_id);

-- index idx_site_domains_primary
CREATE UNIQUE INDEX idx_site_domains_primary ON public.site_domains USING btree (location_id) WHERE is_primary;

-- index idx_site_images_location
CREATE INDEX idx_site_images_location ON public.site_images USING btree (location_id);

-- index idx_square_connections_merchant
CREATE INDEX idx_square_connections_merchant ON public.square_connections USING btree (merchant_id);

-- index idx_staff_journal_location_at
CREATE INDEX idx_staff_journal_location_at ON public.staff_journal USING btree (location_id, at DESC);

-- index idx_staff_location
CREATE INDEX idx_staff_location ON public.staff USING btree (location_id);

-- index idx_tablet_report_tokens_active
CREATE INDEX idx_tablet_report_tokens_active ON public.tablet_report_tokens USING btree (location_id) WHERE (revoked_at IS NULL);

-- index idx_tenants_store_id
CREATE UNIQUE INDEX idx_tenants_store_id ON public.tenants USING btree (store_id) WHERE (store_id IS NOT NULL);

-- index idx_tenants_username
CREATE UNIQUE INDEX idx_tenants_username ON public.tenants USING btree (username) WHERE (username IS NOT NULL);

-- index idx_z_report_versions_batch
CREATE INDEX idx_z_report_versions_batch ON public.z_report_versions USING btree (location_id, batch_id, seq);

-- index idx_z_report_versions_id
CREATE INDEX idx_z_report_versions_id ON public.z_report_versions USING btree (location_id, id);

-- index idx_z_reports_date
CREATE INDEX idx_z_reports_date ON public.z_reports USING btree (location_id, business_date, closed_at DESC);

-- index payments_client_key_uq
CREATE UNIQUE INDEX payments_client_key_uq ON public.payments USING btree (location_id, client_key) WHERE (client_key <> ''::text);

-- index payments_counter_ref_uq
CREATE UNIQUE INDEX payments_counter_ref_uq ON public.payments USING btree (location_id, processor, token) WHERE ((origin = 'counter'::text) AND (token <> ''::text) AND (amount_cents > 0));

-- trigger card_holds audit_card_holds
CREATE TRIGGER audit_card_holds AFTER INSERT OR DELETE OR UPDATE ON public.card_holds FOR EACH ROW EXECUTE FUNCTION public.audit_row('key=order_id', 'ignore=updated_at,next_try_at,attempts,last_error');

-- trigger channels audit_channels
CREATE TRIGGER audit_channels AFTER INSERT OR DELETE OR UPDATE ON public.channels FOR EACH ROW EXECUTE FUNCTION public.audit_row('mask=key_hash', 'ignore=last_used_at,updated_at');

-- trigger deals audit_deals
CREATE TRIGGER audit_deals AFTER INSERT OR DELETE OR UPDATE ON public.deals FOR EACH ROW EXECUTE FUNCTION public.audit_row('ignore=updated_at');

-- trigger devices audit_devices
CREATE TRIGGER audit_devices AFTER INSERT OR DELETE OR UPDATE ON public.devices FOR EACH ROW EXECUTE FUNCTION public.audit_row('mask=key_hash', 'ignore=last_used_at');

-- trigger drawer_movements audit_drawer_movements
CREATE TRIGGER audit_drawer_movements AFTER INSERT OR DELETE OR UPDATE ON public.drawer_movements FOR EACH ROW EXECUTE FUNCTION public.audit_row();

-- trigger drawer_session_revisions audit_drawer_session_revisions
CREATE TRIGGER audit_drawer_session_revisions AFTER DELETE ON public.drawer_session_revisions FOR EACH ROW EXECUTE FUNCTION public.audit_row('key=seq');

-- trigger drawer_sessions audit_drawer_sessions
CREATE TRIGGER audit_drawer_sessions AFTER INSERT OR DELETE OR UPDATE ON public.drawer_sessions FOR EACH ROW EXECUTE FUNCTION public.audit_row('ignore=updated_at');

-- trigger item_modifier_groups audit_item_modifier_groups
CREATE TRIGGER audit_item_modifier_groups AFTER INSERT OR DELETE OR UPDATE ON public.item_modifier_groups FOR EACH ROW EXECUTE FUNCTION public.audit_row('key=item_id,group_id');

-- trigger locations audit_locations
CREATE TRIGGER audit_locations AFTER INSERT OR DELETE OR UPDATE ON public.locations FOR EACH ROW EXECUTE FUNCTION public.audit_row();

-- trigger audit_log audit_log_append_only
CREATE TRIGGER audit_log_append_only BEFORE DELETE OR UPDATE ON public.audit_log FOR EACH ROW EXECUTE FUNCTION public.audit_log_append_only();

-- trigger audit_log audit_log_no_truncate
CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON public.audit_log FOR EACH STATEMENT EXECUTE FUNCTION public.audit_log_append_only();

-- trigger menu_categories audit_menu_categories
CREATE TRIGGER audit_menu_categories AFTER INSERT OR DELETE OR UPDATE ON public.menu_categories FOR EACH ROW EXECUTE FUNCTION public.audit_row('ignore=updated_at');

-- trigger menu_families audit_menu_families
CREATE TRIGGER audit_menu_families AFTER INSERT OR DELETE OR UPDATE ON public.menu_families FOR EACH ROW EXECUTE FUNCTION public.audit_row('ignore=updated_at');

-- trigger menu_item_sizes audit_menu_item_sizes
CREATE TRIGGER audit_menu_item_sizes AFTER INSERT OR DELETE OR UPDATE ON public.menu_item_sizes FOR EACH ROW EXECUTE FUNCTION public.audit_row('key=item_id,size_id');

-- trigger menu_items audit_menu_items
CREATE TRIGGER audit_menu_items AFTER INSERT OR DELETE OR UPDATE ON public.menu_items FOR EACH ROW EXECUTE FUNCTION public.audit_row('ignore=updated_at');

-- trigger menu_sizes audit_menu_sizes
CREATE TRIGGER audit_menu_sizes AFTER INSERT OR DELETE OR UPDATE ON public.menu_sizes FOR EACH ROW EXECUTE FUNCTION public.audit_row('ignore=updated_at');

-- trigger menu_view_entries audit_menu_view_entries
CREATE TRIGGER audit_menu_view_entries AFTER INSERT OR DELETE OR UPDATE ON public.menu_view_entries FOR EACH ROW EXECUTE FUNCTION public.audit_row('ignore=updated_at');

-- trigger menu_views audit_menu_views
CREATE TRIGGER audit_menu_views AFTER INSERT OR DELETE OR UPDATE ON public.menu_views FOR EACH ROW EXECUTE FUNCTION public.audit_row('mask=layout', 'ignore=updated_at');

-- trigger menus audit_menus
CREATE TRIGGER audit_menus AFTER INSERT OR DELETE OR UPDATE ON public.menus FOR EACH ROW EXECUTE FUNCTION public.audit_row('ignore=updated_at');

-- trigger modifier_groups audit_modifier_groups
CREATE TRIGGER audit_modifier_groups AFTER INSERT OR DELETE OR UPDATE ON public.modifier_groups FOR EACH ROW EXECUTE FUNCTION public.audit_row('ignore=updated_at');

-- trigger modifier_option_prices audit_modifier_option_prices
CREATE TRIGGER audit_modifier_option_prices AFTER INSERT OR DELETE OR UPDATE ON public.modifier_option_prices FOR EACH ROW EXECUTE FUNCTION public.audit_row('key=option_id,size_id');

-- trigger modifier_options audit_modifier_options
CREATE TRIGGER audit_modifier_options AFTER INSERT OR DELETE OR UPDATE ON public.modifier_options FOR EACH ROW EXECUTE FUNCTION public.audit_row('ignore=updated_at');

-- trigger money_flags audit_money_flags
CREATE TRIGGER audit_money_flags AFTER INSERT OR DELETE OR UPDATE ON public.money_flags FOR EACH ROW EXECUTE FUNCTION public.audit_row();

-- trigger operator_locations audit_operator_locations
CREATE TRIGGER audit_operator_locations AFTER INSERT OR DELETE OR UPDATE ON public.operator_locations FOR EACH ROW EXECUTE FUNCTION public.audit_row('key=operator_id,location_id');

-- trigger operator_tokens audit_operator_tokens
CREATE TRIGGER audit_operator_tokens AFTER INSERT OR DELETE OR UPDATE ON public.operator_tokens FOR EACH ROW EXECUTE FUNCTION public.audit_row('mask=token_hash', 'ignore=last_used_at');

-- trigger operators audit_operators
CREATE TRIGGER audit_operators AFTER INSERT OR DELETE OR UPDATE ON public.operators FOR EACH ROW EXECUTE FUNCTION public.audit_row('mask=password_hash', 'ignore=updated_at');

-- trigger order_money_changes audit_order_money_changes
CREATE TRIGGER audit_order_money_changes AFTER DELETE ON public.order_money_changes FOR EACH ROW EXECUTE FUNCTION public.audit_row();

-- trigger orders audit_orders_delete
CREATE TRIGGER audit_orders_delete AFTER DELETE ON public.orders FOR EACH ROW EXECUTE FUNCTION public.audit_row('only=source,source_event_id,status,subtotal_cents,tax_cents,tip_cents,total_cents,fees,discounts,placed_at,agent_batch_id,tenant_id,location_id');

-- trigger orders audit_orders_money
CREATE TRIGGER audit_orders_money AFTER UPDATE ON public.orders FOR EACH ROW WHEN (((old.subtotal_cents IS DISTINCT FROM new.subtotal_cents) OR (old.tax_cents IS DISTINCT FROM new.tax_cents) OR (old.tip_cents IS DISTINCT FROM new.tip_cents) OR (old.total_cents IS DISTINCT FROM new.total_cents) OR (old.fees IS DISTINCT FROM new.fees) OR (old.discounts IS DISTINCT FROM new.discounts) OR ((old.status IS DISTINCT FROM new.status) AND ((new.status = ANY (ARRAY['cancelled'::text, 'rejected'::text])) OR (old.status = ANY (ARRAY['cancelled'::text, 'rejected'::text])))))) EXECUTE FUNCTION public.audit_row('only=status,subtotal_cents,tax_cents,tip_cents,total_cents,fees,discounts,tenant_id,location_id');

-- trigger payments audit_payments
CREATE TRIGGER audit_payments AFTER INSERT OR DELETE OR UPDATE ON public.payments FOR EACH ROW EXECUTE FUNCTION public.audit_row();

-- trigger platform_keys audit_platform_keys
CREATE TRIGGER audit_platform_keys AFTER INSERT OR DELETE OR UPDATE ON public.platform_keys FOR EACH ROW EXECUTE FUNCTION public.audit_row('mask=key_hash', 'ignore=last_used_at');

-- trigger pricing_settings audit_pricing_settings
CREATE TRIGGER audit_pricing_settings AFTER INSERT OR DELETE OR UPDATE ON public.pricing_settings FOR EACH ROW EXECUTE FUNCTION public.audit_row('key=location_id,kind', 'ignore=updated_at');

-- trigger printers audit_printers
CREATE TRIGGER audit_printers AFTER INSERT OR DELETE OR UPDATE ON public.printers FOR EACH ROW EXECUTE FUNCTION public.audit_row('ignore=updated_at');

-- trigger site_domains audit_site_domains
CREATE TRIGGER audit_site_domains AFTER INSERT OR DELETE OR UPDATE ON public.site_domains FOR EACH ROW EXECUTE FUNCTION public.audit_row('key=host');

-- trigger site_settings audit_site_settings
CREATE TRIGGER audit_site_settings AFTER INSERT OR DELETE OR UPDATE ON public.site_settings FOR EACH ROW EXECUTE FUNCTION public.audit_row('key=location_id', 'ignore=updated_at');

-- trigger square_connections audit_square_connections
CREATE TRIGGER audit_square_connections AFTER INSERT OR DELETE OR UPDATE ON public.square_connections FOR EACH ROW EXECUTE FUNCTION public.audit_row('key=location_id', 'mask=access_token_enc,refresh_token_enc', 'ignore=refreshed_at,expires_at');

-- trigger staff audit_staff
CREATE TRIGGER audit_staff AFTER INSERT OR DELETE OR UPDATE ON public.staff FOR EACH ROW EXECUTE FUNCTION public.audit_row('mask=pin_hash', 'ignore=updated_at');

-- trigger staff_journal audit_staff_journal
CREATE TRIGGER audit_staff_journal AFTER DELETE ON public.staff_journal FOR EACH ROW EXECUTE FUNCTION public.audit_row();

-- trigger store_hours_exceptions audit_store_hours_exceptions
CREATE TRIGGER audit_store_hours_exceptions AFTER INSERT OR DELETE OR UPDATE ON public.store_hours_exceptions FOR EACH ROW EXECUTE FUNCTION public.audit_row();

-- trigger store_ordering audit_store_ordering
CREATE TRIGGER audit_store_ordering AFTER INSERT OR DELETE OR UPDATE ON public.store_ordering FOR EACH ROW EXECUTE FUNCTION public.audit_row('key=location_id', 'ignore=updated_at,restocked_at');

-- trigger tablet_report_tokens audit_tablet_report_tokens
CREATE TRIGGER audit_tablet_report_tokens AFTER INSERT OR DELETE OR UPDATE ON public.tablet_report_tokens FOR EACH ROW EXECUTE FUNCTION public.audit_row('mask=token_hash', 'ignore=last_used_at');

-- trigger tenants audit_tenants
CREATE TRIGGER audit_tenants AFTER INSERT OR DELETE OR UPDATE ON public.tenants FOR EACH ROW EXECUTE FUNCTION public.audit_row('mask=password_hash');

-- trigger terminal_settings audit_terminal_settings
CREATE TRIGGER audit_terminal_settings AFTER INSERT OR DELETE OR UPDATE ON public.terminal_settings FOR EACH ROW EXECUTE FUNCTION public.audit_row('key=location_id', 'ignore=updated_at,version');

-- trigger z_report_versions audit_z_report_versions
CREATE TRIGGER audit_z_report_versions AFTER DELETE ON public.z_report_versions FOR EACH ROW EXECUTE FUNCTION public.audit_row('key=seq');

-- trigger z_reports audit_z_reports
CREATE TRIGGER audit_z_reports AFTER INSERT OR DELETE OR UPDATE ON public.z_reports FOR EACH ROW EXECUTE FUNCTION public.audit_row();

-- trigger orders card_holds_on_order_cancel
CREATE TRIGGER card_holds_on_order_cancel AFTER UPDATE OF status ON public.orders FOR EACH ROW EXECUTE FUNCTION public.card_holds_on_order_cancel();

-- trigger customer_addresses customer_addresses_touch
CREATE TRIGGER customer_addresses_touch AFTER INSERT OR DELETE OR UPDATE ON public.customer_addresses FOR EACH ROW EXECUTE FUNCTION public.customer_addresses_touch();

-- trigger customers customers_tombstone
CREATE TRIGGER customers_tombstone AFTER DELETE ON public.customers FOR EACH ROW EXECUTE FUNCTION public.customers_tombstone();

-- trigger customers customers_touch
CREATE TRIGGER customers_touch BEFORE UPDATE ON public.customers FOR EACH ROW EXECUTE FUNCTION public.customers_touch();

-- trigger payments payments_fill_location
CREATE TRIGGER payments_fill_location BEFORE INSERT ON public.payments FOR EACH ROW EXECUTE FUNCTION public.payments_fill_location();

-- trigger deals trg_menu_version
CREATE TRIGGER trg_menu_version AFTER INSERT OR DELETE OR UPDATE ON public.deals FOR EACH ROW EXECUTE FUNCTION public.bump_menu_version();

-- trigger item_modifier_groups trg_menu_version
CREATE TRIGGER trg_menu_version AFTER INSERT OR DELETE OR UPDATE ON public.item_modifier_groups FOR EACH ROW EXECUTE FUNCTION public.bump_menu_version();

-- trigger menu_categories trg_menu_version
CREATE TRIGGER trg_menu_version AFTER INSERT OR DELETE OR UPDATE ON public.menu_categories FOR EACH ROW EXECUTE FUNCTION public.bump_menu_version();

-- trigger menu_families trg_menu_version
CREATE TRIGGER trg_menu_version AFTER INSERT OR DELETE OR UPDATE ON public.menu_families FOR EACH ROW EXECUTE FUNCTION public.bump_menu_version();

-- trigger menu_item_sizes trg_menu_version
CREATE TRIGGER trg_menu_version AFTER INSERT OR DELETE OR UPDATE ON public.menu_item_sizes FOR EACH ROW EXECUTE FUNCTION public.bump_menu_version();

-- trigger menu_items trg_menu_version
CREATE TRIGGER trg_menu_version AFTER INSERT OR DELETE OR UPDATE ON public.menu_items FOR EACH ROW EXECUTE FUNCTION public.bump_menu_version();

-- trigger menu_sizes trg_menu_version
CREATE TRIGGER trg_menu_version AFTER INSERT OR DELETE OR UPDATE ON public.menu_sizes FOR EACH ROW EXECUTE FUNCTION public.bump_menu_version();

-- trigger menu_view_entries trg_menu_version
CREATE TRIGGER trg_menu_version AFTER INSERT OR DELETE OR UPDATE ON public.menu_view_entries FOR EACH ROW EXECUTE FUNCTION public.bump_menu_version();

-- trigger menu_views trg_menu_version
CREATE TRIGGER trg_menu_version AFTER INSERT OR DELETE OR UPDATE ON public.menu_views FOR EACH ROW EXECUTE FUNCTION public.bump_menu_version();

-- trigger menus trg_menu_version
CREATE TRIGGER trg_menu_version AFTER INSERT OR DELETE OR UPDATE ON public.menus FOR EACH ROW EXECUTE FUNCTION public.bump_menu_version();

-- trigger modifier_groups trg_menu_version
CREATE TRIGGER trg_menu_version AFTER INSERT OR DELETE OR UPDATE ON public.modifier_groups FOR EACH ROW EXECUTE FUNCTION public.bump_menu_version();

-- trigger modifier_option_prices trg_menu_version
CREATE TRIGGER trg_menu_version AFTER INSERT OR DELETE OR UPDATE ON public.modifier_option_prices FOR EACH ROW EXECUTE FUNCTION public.bump_menu_version();

-- trigger modifier_options trg_menu_version
CREATE TRIGGER trg_menu_version AFTER INSERT OR DELETE OR UPDATE ON public.modifier_options FOR EACH ROW EXECUTE FUNCTION public.bump_menu_version();

-- trigger pricing_settings trg_menu_version
CREATE TRIGGER trg_menu_version AFTER INSERT OR DELETE OR UPDATE ON public.pricing_settings FOR EACH ROW EXECUTE FUNCTION public.bump_menu_version();

-- trigger channels trg_menu_version_channel_view
CREATE TRIGGER trg_menu_version_channel_view AFTER UPDATE OF view_id ON public.channels FOR EACH ROW WHEN ((old.view_id IS DISTINCT FROM new.view_id)) EXECUTE FUNCTION public.bump_menu_version();

-- fk constraint agent_backups agent_backups_location_id_tenant_id_fkey
ALTER TABLE ONLY public.agent_backups
    ADD CONSTRAINT agent_backups_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint agent_health agent_health_location_id_tenant_id_fkey
ALTER TABLE ONLY public.agent_health
    ADD CONSTRAINT agent_health_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint agent_state agent_state_location_id_tenant_id_fkey
ALTER TABLE ONLY public.agent_state
    ADD CONSTRAINT agent_state_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint agent_updates agent_updates_location_id_tenant_id_fkey
ALTER TABLE ONLY public.agent_updates
    ADD CONSTRAINT agent_updates_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint card_holds card_holds_location_id_tenant_id_fkey
ALTER TABLE ONLY public.card_holds
    ADD CONSTRAINT card_holds_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE RESTRICT;

-- fk constraint card_holds card_holds_order_id_fkey
ALTER TABLE ONLY public.card_holds
    ADD CONSTRAINT card_holds_order_id_fkey FOREIGN KEY (order_id) REFERENCES public.orders(id) ON DELETE RESTRICT;

-- fk constraint card_holds card_holds_payment_id_fkey
ALTER TABLE ONLY public.card_holds
    ADD CONSTRAINT card_holds_payment_id_fkey FOREIGN KEY (payment_id) REFERENCES public.payments(id);

-- fk constraint card_holds card_holds_tip_payment_id_fkey
ALTER TABLE ONLY public.card_holds
    ADD CONSTRAINT card_holds_tip_payment_id_fkey FOREIGN KEY (tip_payment_id) REFERENCES public.payments(id);

-- fk constraint channel_events channel_events_channel_id_fkey
ALTER TABLE ONLY public.channel_events
    ADD CONSTRAINT channel_events_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id) ON DELETE CASCADE;

-- fk constraint channel_orders channel_orders_channel_id_fkey
ALTER TABLE ONLY public.channel_orders
    ADD CONSTRAINT channel_orders_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id) ON DELETE SET NULL;

-- fk constraint channel_orders channel_orders_customer_id_fkey
ALTER TABLE ONLY public.channel_orders
    ADD CONSTRAINT channel_orders_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE SET NULL;

-- fk constraint channel_orders channel_orders_operator_id_fkey
ALTER TABLE ONLY public.channel_orders
    ADD CONSTRAINT channel_orders_operator_id_fkey FOREIGN KEY (operator_id) REFERENCES public.operators(id) ON DELETE SET NULL;

-- fk constraint channel_orders channel_orders_order_id_fkey
ALTER TABLE ONLY public.channel_orders
    ADD CONSTRAINT channel_orders_order_id_fkey FOREIGN KEY (order_id) REFERENCES public.orders(id) ON DELETE RESTRICT;

-- fk constraint channels channels_location_id_tenant_id_fkey
ALTER TABLE ONLY public.channels
    ADD CONSTRAINT channels_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint channels channels_view_id_fkey
ALTER TABLE ONLY public.channels
    ADD CONSTRAINT channels_view_id_fkey FOREIGN KEY (view_id) REFERENCES public.menu_views(id) ON DELETE SET NULL;

-- fk constraint customer_addresses customer_addresses_customer_id_fkey
ALTER TABLE ONLY public.customer_addresses
    ADD CONSTRAINT customer_addresses_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE CASCADE;

-- fk constraint customer_cards customer_cards_customer_id_fkey
ALTER TABLE ONLY public.customer_cards
    ADD CONSTRAINT customer_cards_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE CASCADE;

-- fk constraint customer_sessions customer_sessions_customer_id_fkey
ALTER TABLE ONLY public.customer_sessions
    ADD CONSTRAINT customer_sessions_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE CASCADE;

-- fk constraint customers customers_location_id_tenant_id_fkey
ALTER TABLE ONLY public.customers
    ADD CONSTRAINT customers_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint deals deals_location_id_tenant_id_fkey
ALTER TABLE ONLY public.deals
    ADD CONSTRAINT deals_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint delivery_runs delivery_runs_location_id_tenant_id_fkey
ALTER TABLE ONLY public.delivery_runs
    ADD CONSTRAINT delivery_runs_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint devices devices_location_id_tenant_id_fkey
ALTER TABLE ONLY public.devices
    ADD CONSTRAINT devices_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE RESTRICT;

-- fk constraint diag_reports diag_reports_location_id_tenant_id_fkey
ALTER TABLE ONLY public.diag_reports
    ADD CONSTRAINT diag_reports_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint drawer_movements drawer_movements_location_id_tenant_id_fkey
ALTER TABLE ONLY public.drawer_movements
    ADD CONSTRAINT drawer_movements_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE RESTRICT;

-- fk constraint drawer_session_revisions drawer_session_revisions_location_id_tenant_id_fkey
ALTER TABLE ONLY public.drawer_session_revisions
    ADD CONSTRAINT drawer_session_revisions_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE RESTRICT;

-- fk constraint drawer_sessions drawer_sessions_location_id_tenant_id_fkey
ALTER TABLE ONLY public.drawer_sessions
    ADD CONSTRAINT drawer_sessions_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE RESTRICT;

-- fk constraint driver_shifts driver_shifts_location_id_tenant_id_fkey
ALTER TABLE ONLY public.driver_shifts
    ADD CONSTRAINT driver_shifts_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint drivers drivers_location_id_tenant_id_fkey
ALTER TABLE ONLY public.drivers
    ADD CONSTRAINT drivers_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint email_receipts email_receipts_order_id_fkey
ALTER TABLE ONLY public.email_receipts
    ADD CONSTRAINT email_receipts_order_id_fkey FOREIGN KEY (order_id) REFERENCES public.orders(id) ON DELETE CASCADE;

-- fk constraint item_modifier_groups item_modifier_groups_group_id_tenant_id_fkey
ALTER TABLE ONLY public.item_modifier_groups
    ADD CONSTRAINT item_modifier_groups_group_id_tenant_id_fkey FOREIGN KEY (group_id, tenant_id) REFERENCES public.modifier_groups(id, tenant_id) ON DELETE CASCADE;

-- fk constraint item_modifier_groups item_modifier_groups_item_id_tenant_id_fkey
ALTER TABLE ONLY public.item_modifier_groups
    ADD CONSTRAINT item_modifier_groups_item_id_tenant_id_fkey FOREIGN KEY (item_id, tenant_id) REFERENCES public.menu_items(id, tenant_id) ON DELETE CASCADE;

-- fk constraint line_items line_items_order_id_fkey
ALTER TABLE ONLY public.line_items
    ADD CONSTRAINT line_items_order_id_fkey FOREIGN KEY (order_id) REFERENCES public.orders(id) ON DELETE CASCADE;

-- fk constraint line_items line_items_parent_id_fkey
ALTER TABLE ONLY public.line_items
    ADD CONSTRAINT line_items_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES public.line_items(id) ON DELETE CASCADE;

-- fk constraint locations locations_tenant_id_fkey
ALTER TABLE ONLY public.locations
    ADD CONSTRAINT locations_tenant_id_fkey FOREIGN KEY (tenant_id) REFERENCES public.tenants(id) ON DELETE RESTRICT;

-- fk constraint menu_categories menu_categories_default_printer_id_fkey
ALTER TABLE ONLY public.menu_categories
    ADD CONSTRAINT menu_categories_default_printer_id_fkey FOREIGN KEY (default_printer_id) REFERENCES public.printers(id) ON DELETE SET NULL;

-- fk constraint menu_categories menu_categories_location_id_tenant_id_fkey
ALTER TABLE ONLY public.menu_categories
    ADD CONSTRAINT menu_categories_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint menu_categories menu_categories_menu_id_tenant_id_fkey
ALTER TABLE ONLY public.menu_categories
    ADD CONSTRAINT menu_categories_menu_id_tenant_id_fkey FOREIGN KEY (menu_id, tenant_id) REFERENCES public.menus(id, tenant_id) ON DELETE CASCADE;

-- fk constraint menu_families menu_families_location_id_tenant_id_fkey
ALTER TABLE ONLY public.menu_families
    ADD CONSTRAINT menu_families_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint menu_item_sizes menu_item_sizes_item_id_tenant_id_fkey
ALTER TABLE ONLY public.menu_item_sizes
    ADD CONSTRAINT menu_item_sizes_item_id_tenant_id_fkey FOREIGN KEY (item_id, tenant_id) REFERENCES public.menu_items(id, tenant_id) ON DELETE CASCADE;

-- fk constraint menu_item_sizes menu_item_sizes_size_id_tenant_id_fkey
ALTER TABLE ONLY public.menu_item_sizes
    ADD CONSTRAINT menu_item_sizes_size_id_tenant_id_fkey FOREIGN KEY (size_id, tenant_id) REFERENCES public.menu_sizes(id, tenant_id) ON DELETE CASCADE;

-- fk constraint menu_items menu_items_category_id_tenant_id_fkey
ALTER TABLE ONLY public.menu_items
    ADD CONSTRAINT menu_items_category_id_tenant_id_fkey FOREIGN KEY (category_id, tenant_id) REFERENCES public.menu_categories(id, tenant_id) ON DELETE CASCADE;

-- fk constraint menu_items menu_items_family_id_fkey
ALTER TABLE ONLY public.menu_items
    ADD CONSTRAINT menu_items_family_id_fkey FOREIGN KEY (family_id) REFERENCES public.menu_families(id) ON DELETE SET NULL;

-- fk constraint menu_items menu_items_location_id_tenant_id_fkey
ALTER TABLE ONLY public.menu_items
    ADD CONSTRAINT menu_items_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint menu_items menu_items_printer_id_fkey
ALTER TABLE ONLY public.menu_items
    ADD CONSTRAINT menu_items_printer_id_fkey FOREIGN KEY (printer_id) REFERENCES public.printers(id) ON DELETE SET NULL;

-- fk constraint menu_sizes menu_sizes_location_id_tenant_id_fkey
ALTER TABLE ONLY public.menu_sizes
    ADD CONSTRAINT menu_sizes_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint menu_versions menu_versions_location_id_fkey
ALTER TABLE ONLY public.menu_versions
    ADD CONSTRAINT menu_versions_location_id_fkey FOREIGN KEY (location_id) REFERENCES public.locations(id) ON DELETE CASCADE;

-- fk constraint menu_view_builds menu_view_builds_view_id_fkey
ALTER TABLE ONLY public.menu_view_builds
    ADD CONSTRAINT menu_view_builds_view_id_fkey FOREIGN KEY (view_id) REFERENCES public.menu_views(id) ON DELETE CASCADE;

-- fk constraint menu_view_entries menu_view_entries_view_id_tenant_id_fkey
ALTER TABLE ONLY public.menu_view_entries
    ADD CONSTRAINT menu_view_entries_view_id_tenant_id_fkey FOREIGN KEY (view_id, tenant_id) REFERENCES public.menu_views(id, tenant_id) ON DELETE CASCADE;

-- fk constraint menu_views menu_views_location_id_tenant_id_fkey
ALTER TABLE ONLY public.menu_views
    ADD CONSTRAINT menu_views_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint menus menus_location_id_tenant_id_fkey
ALTER TABLE ONLY public.menus
    ADD CONSTRAINT menus_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint modifier_groups modifier_groups_location_id_tenant_id_fkey
ALTER TABLE ONLY public.modifier_groups
    ADD CONSTRAINT modifier_groups_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint modifier_option_prices modifier_option_prices_option_id_tenant_id_fkey
ALTER TABLE ONLY public.modifier_option_prices
    ADD CONSTRAINT modifier_option_prices_option_id_tenant_id_fkey FOREIGN KEY (option_id, tenant_id) REFERENCES public.modifier_options(id, tenant_id) ON DELETE CASCADE;

-- fk constraint modifier_option_prices modifier_option_prices_size_id_tenant_id_fkey
ALTER TABLE ONLY public.modifier_option_prices
    ADD CONSTRAINT modifier_option_prices_size_id_tenant_id_fkey FOREIGN KEY (size_id, tenant_id) REFERENCES public.menu_sizes(id, tenant_id) ON DELETE CASCADE;

-- fk constraint modifier_options modifier_options_family_id_fkey
ALTER TABLE ONLY public.modifier_options
    ADD CONSTRAINT modifier_options_family_id_fkey FOREIGN KEY (family_id) REFERENCES public.menu_families(id) ON DELETE SET NULL;

-- fk constraint modifier_options modifier_options_group_id_tenant_id_fkey
ALTER TABLE ONLY public.modifier_options
    ADD CONSTRAINT modifier_options_group_id_tenant_id_fkey FOREIGN KEY (group_id, tenant_id) REFERENCES public.modifier_groups(id, tenant_id) ON DELETE CASCADE;

-- fk constraint money_flags money_flags_location_id_tenant_id_fkey
ALTER TABLE ONLY public.money_flags
    ADD CONSTRAINT money_flags_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE RESTRICT;

-- fk constraint operator_locations operator_locations_location_id_tenant_id_fkey
ALTER TABLE ONLY public.operator_locations
    ADD CONSTRAINT operator_locations_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint operator_locations operator_locations_operator_id_fkey
ALTER TABLE ONLY public.operator_locations
    ADD CONSTRAINT operator_locations_operator_id_fkey FOREIGN KEY (operator_id) REFERENCES public.operators(id) ON DELETE CASCADE;

-- fk constraint operator_tokens operator_tokens_operator_id_fkey
ALTER TABLE ONLY public.operator_tokens
    ADD CONSTRAINT operator_tokens_operator_id_fkey FOREIGN KEY (operator_id) REFERENCES public.operators(id) ON DELETE CASCADE;

-- fk constraint order_geo order_geo_order_id_fkey
ALTER TABLE ONLY public.order_geo
    ADD CONSTRAINT order_geo_order_id_fkey FOREIGN KEY (order_id) REFERENCES public.orders(id) ON DELETE CASCADE;

-- fk constraint order_placements order_placements_channel_id_fkey
ALTER TABLE ONLY public.order_placements
    ADD CONSTRAINT order_placements_channel_id_fkey FOREIGN KEY (channel_id) REFERENCES public.channels(id) ON DELETE RESTRICT;

-- fk constraint orders orders_location_id_tenant_id_fkey
ALTER TABLE ONLY public.orders
    ADD CONSTRAINT orders_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE RESTRICT;

-- fk constraint outbox_dispatches outbox_dispatches_event_id_fkey
ALTER TABLE ONLY public.outbox_dispatches
    ADD CONSTRAINT outbox_dispatches_event_id_fkey FOREIGN KEY (event_id) REFERENCES public.outbox_events(id) ON DELETE CASCADE;

-- fk constraint payments payments_location_id_fkey
ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_location_id_fkey FOREIGN KEY (location_id) REFERENCES public.locations(id) ON DELETE RESTRICT;

-- fk constraint payments payments_order_id_fkey
ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_order_id_fkey FOREIGN KEY (order_id) REFERENCES public.orders(id) ON DELETE RESTRICT;

-- fk constraint payments payments_refund_of_fkey
ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_refund_of_fkey FOREIGN KEY (refund_of) REFERENCES public.payments(id);

-- fk constraint pricing_settings pricing_settings_location_id_tenant_id_fkey
ALTER TABLE ONLY public.pricing_settings
    ADD CONSTRAINT pricing_settings_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint printer_ip_changes printer_ip_changes_location_id_tenant_id_fkey
ALTER TABLE ONLY public.printer_ip_changes
    ADD CONSTRAINT printer_ip_changes_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint printer_ip_changes printer_ip_changes_printer_id_fkey
ALTER TABLE ONLY public.printer_ip_changes
    ADD CONSTRAINT printer_ip_changes_printer_id_fkey FOREIGN KEY (printer_id) REFERENCES public.printers(id) ON DELETE CASCADE;

-- fk constraint printers printers_location_id_tenant_id_fkey
ALTER TABLE ONLY public.printers
    ADD CONSTRAINT printers_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint site_domains site_domains_location_id_tenant_id_fkey
ALTER TABLE ONLY public.site_domains
    ADD CONSTRAINT site_domains_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint site_images site_images_location_id_tenant_id_fkey
ALTER TABLE ONLY public.site_images
    ADD CONSTRAINT site_images_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint site_settings site_settings_location_id_tenant_id_fkey
ALTER TABLE ONLY public.site_settings
    ADD CONSTRAINT site_settings_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint square_connections square_connections_location_id_tenant_id_fkey
ALTER TABLE ONLY public.square_connections
    ADD CONSTRAINT square_connections_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint staff_journal staff_journal_location_id_tenant_id_fkey
ALTER TABLE ONLY public.staff_journal
    ADD CONSTRAINT staff_journal_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE RESTRICT;

-- fk constraint staff staff_location_id_tenant_id_fkey
ALTER TABLE ONLY public.staff
    ADD CONSTRAINT staff_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint status_events status_events_order_id_fkey
ALTER TABLE ONLY public.status_events
    ADD CONSTRAINT status_events_order_id_fkey FOREIGN KEY (order_id) REFERENCES public.orders(id) ON DELETE CASCADE;

-- fk constraint store_hours_exceptions store_hours_exceptions_location_id_tenant_id_fkey
ALTER TABLE ONLY public.store_hours_exceptions
    ADD CONSTRAINT store_hours_exceptions_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint store_ordering store_ordering_location_id_tenant_id_fkey
ALTER TABLE ONLY public.store_ordering
    ADD CONSTRAINT store_ordering_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint tablet_report_tokens tablet_report_tokens_location_id_tenant_id_fkey
ALTER TABLE ONLY public.tablet_report_tokens
    ADD CONSTRAINT tablet_report_tokens_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint terminal_settings terminal_settings_location_id_tenant_id_fkey
ALTER TABLE ONLY public.terminal_settings
    ADD CONSTRAINT terminal_settings_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE CASCADE;

-- fk constraint z_report_versions z_report_versions_location_id_tenant_id_fkey
ALTER TABLE ONLY public.z_report_versions
    ADD CONSTRAINT z_report_versions_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE RESTRICT;

-- fk constraint z_reports z_reports_location_id_tenant_id_fkey
ALTER TABLE ONLY public.z_reports
    ADD CONSTRAINT z_reports_location_id_tenant_id_fkey FOREIGN KEY (location_id, tenant_id) REFERENCES public.locations(id, tenant_id) ON DELETE RESTRICT;


--
--

INSERT INTO public.schema_baseline (baseline, squashed_from) VALUES ('2026-11-12', 48);

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

-- 00002_printer_print_columns: a printer's print width in font-A characters
-- (48 = 576 dots, 42 = 512, 32 = 384); the Pi lays every slip out for it.
ALTER TABLE public.printers ADD COLUMN print_columns smallint NOT NULL DEFAULT 48 CONSTRAINT printers_print_columns_check CHECK (print_columns IN (32, 42, 48));

-- 00003_callcenter: the staff role callcenter + the call center's browsers (device
-- tokens; the agent's PIN session on the row). contract/main.md §2.5zh.
ALTER TABLE public.staff DROP CONSTRAINT staff_role_check;
ALTER TABLE public.staff ADD CONSTRAINT staff_role_check CHECK ((role = ANY (ARRAY['cashier'::text, 'manager'::text, 'driver'::text, 'callcenter'::text])));
CREATE TABLE public.callcenter_devices (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    name text NOT NULL,
    token_hash text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone,
    revoked_at timestamp with time zone,
    staff_id uuid,
    session_started_at timestamp with time zone,
    session_seen_at timestamp with time zone,
    CONSTRAINT callcenter_devices_pkey PRIMARY KEY (id),
    CONSTRAINT callcenter_devices_token_hash_key UNIQUE (token_hash),
    CONSTRAINT callcenter_devices_location_fkey FOREIGN KEY (location_id) REFERENCES public.locations(id) ON DELETE CASCADE
);
CREATE INDEX idx_callcenter_devices_location ON public.callcenter_devices USING btree (location_id, created_at DESC);
CREATE TRIGGER audit_callcenter_devices AFTER INSERT OR DELETE OR UPDATE ON public.callcenter_devices FOR EACH ROW EXECUTE FUNCTION public.audit_row('mask=token_hash', 'ignore=last_seen_at,session_seen_at');

-- 00004_callcenter_pin_guard: wrong PINs counted on the device row; explicit grants.
ALTER TABLE public.callcenter_devices ADD COLUMN pin_fails integer DEFAULT 0 NOT NULL;
ALTER TABLE public.callcenter_devices ADD COLUMN pin_locked_until timestamp with time zone;
DROP TRIGGER audit_callcenter_devices ON public.callcenter_devices;
CREATE TRIGGER audit_callcenter_devices AFTER INSERT OR DELETE OR UPDATE ON public.callcenter_devices FOR EACH ROW EXECUTE FUNCTION public.audit_row('mask=token_hash', 'ignore=last_seen_at,session_seen_at,pin_fails,pin_locked_until');
GRANT SELECT, INSERT, UPDATE, DELETE ON public.callcenter_devices TO quicc_app;
GRANT SELECT ON public.callcenter_devices TO quicc_readonly;
