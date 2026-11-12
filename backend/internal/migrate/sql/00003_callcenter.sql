-- The call center's remote terminal (CALLCENTER-PLAN.md, contract/main.md
-- §2.5zh, 2026-11-12): the staff role `callcenter` (its PINs work only in the
-- call-center app) and the browsers the store's login opened the app on (one
-- long-lived device token each, revocable in Store › Staff; the agent's PIN
-- session lives on the device row).

-- +goose Up
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

-- +goose Down
DROP TABLE public.callcenter_devices;
UPDATE public.staff SET active = false, role = 'cashier' WHERE role = 'callcenter';
ALTER TABLE public.staff DROP CONSTRAINT staff_role_check;
ALTER TABLE public.staff ADD CONSTRAINT staff_role_check CHECK ((role = ANY (ARRAY['cashier'::text, 'manager'::text, 'driver'::text])));
