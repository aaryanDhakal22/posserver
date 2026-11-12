-- The call center's own shared login (security review H1, contract/main.md
-- §2.5zh): a username + password the owner sets in Store › Staff, valid ONLY at
-- /callcenter/v1/login. The store manager's login no longer opens the
-- call-center app (it also opens Store pages and links a Pi). And the PIN
-- guard's window (M1): when the last PIN attempt was counted, so failures decay
-- with time instead of being reset by a right PIN.

-- +goose Up
CREATE TABLE public.callcenter_logins (
    location_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    username text NOT NULL,
    password_hash text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT callcenter_logins_pkey PRIMARY KEY (location_id),
    CONSTRAINT callcenter_logins_location_fkey FOREIGN KEY (location_id) REFERENCES public.locations(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX callcenter_logins_username_uq ON public.callcenter_logins USING btree (lower(username));
CREATE TRIGGER audit_callcenter_logins AFTER INSERT OR DELETE OR UPDATE ON public.callcenter_logins FOR EACH ROW EXECUTE FUNCTION public.audit_row('key=location_id', 'mask=password_hash', 'ignore=updated_at');
GRANT SELECT, INSERT, UPDATE, DELETE ON public.callcenter_logins TO quicc_app;
GRANT SELECT ON public.callcenter_logins TO quicc_readonly;
ALTER TABLE public.callcenter_devices ADD COLUMN pin_failed_at timestamp with time zone;
DROP TRIGGER audit_callcenter_devices ON public.callcenter_devices;
CREATE TRIGGER audit_callcenter_devices AFTER INSERT OR DELETE OR UPDATE ON public.callcenter_devices FOR EACH ROW EXECUTE FUNCTION public.audit_row('mask=token_hash', 'ignore=last_seen_at,session_seen_at,pin_fails,pin_locked_until,pin_failed_at');

-- +goose Down
DROP TRIGGER audit_callcenter_devices ON public.callcenter_devices;
CREATE TRIGGER audit_callcenter_devices AFTER INSERT OR DELETE OR UPDATE ON public.callcenter_devices FOR EACH ROW EXECUTE FUNCTION public.audit_row('mask=token_hash', 'ignore=last_seen_at,session_seen_at,pin_fails,pin_locked_until');
ALTER TABLE public.callcenter_devices DROP COLUMN pin_failed_at;
DROP TABLE public.callcenter_logins;
