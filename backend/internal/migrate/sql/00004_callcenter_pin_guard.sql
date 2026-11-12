-- The call center's PIN guard (contract/main.md §2.5zh "Agent session"): wrong
-- PINs are counted on the device row (they survive a main restart and the
-- blue-green swap), and the table's grants are spelled out (the baseline's
-- default privileges only cover tables made by the role that ran it).

-- +goose Up
ALTER TABLE public.callcenter_devices ADD COLUMN pin_fails integer DEFAULT 0 NOT NULL;
ALTER TABLE public.callcenter_devices ADD COLUMN pin_locked_until timestamp with time zone;
DROP TRIGGER audit_callcenter_devices ON public.callcenter_devices;
CREATE TRIGGER audit_callcenter_devices AFTER INSERT OR DELETE OR UPDATE ON public.callcenter_devices FOR EACH ROW EXECUTE FUNCTION public.audit_row('mask=token_hash', 'ignore=last_seen_at,session_seen_at,pin_fails,pin_locked_until');
GRANT SELECT, INSERT, UPDATE, DELETE ON public.callcenter_devices TO quicc_app;
GRANT SELECT ON public.callcenter_devices TO quicc_readonly;

-- +goose Down
DROP TRIGGER audit_callcenter_devices ON public.callcenter_devices;
CREATE TRIGGER audit_callcenter_devices AFTER INSERT OR DELETE OR UPDATE ON public.callcenter_devices FOR EACH ROW EXECUTE FUNCTION public.audit_row('mask=token_hash', 'ignore=last_seen_at,session_seen_at');
ALTER TABLE public.callcenter_devices DROP COLUMN pin_locked_until;
ALTER TABLE public.callcenter_devices DROP COLUMN pin_fails;
