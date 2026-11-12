-- +goose Up
-- +goose StatementBegin
-- 00043's SQL functions, schema-qualified (pg_restore runs them with an empty
-- search_path: money_fee_cents couldn't find money_cents and a dump of main
-- didn't restore), and the card-hold cancel trigger for a database that ran
-- 00043 before it had one. Idempotent: a database that ran today's 00043
-- ends up the same.
CREATE OR REPLACE FUNCTION money_fee_cents(fees jsonb, typ text, field text) RETURNS bigint LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $f$
    SELECT COALESCE(sum(COALESCE(public.money_cents(f ->> field), 0)), 0)::bigint
    FROM jsonb_array_elements(CASE WHEN jsonb_typeof(fees) = 'array' THEN fees ELSE '[]'::jsonb END) f
    WHERE typ = '' OR f ->> 'Type' = typ $f$;
CREATE OR REPLACE FUNCTION payments_fill_location() RETURNS trigger LANGUAGE plpgsql SET search_path = public AS $f$
BEGIN
    IF NEW.location_id IS NULL THEN
        SELECT location_id INTO NEW.location_id FROM public.orders WHERE id = NEW.order_id;
    END IF;
    RETURN NEW;
END $f$;
CREATE OR REPLACE FUNCTION card_holds_on_order_cancel() RETURNS trigger LANGUAGE plpgsql SET search_path = public AS $f$
BEGIN
    IF NEW.status IN ('cancelled', 'rejected') AND OLD.status IS DISTINCT FROM NEW.status THEN
        UPDATE public.card_holds SET cancel_requested_at = COALESCE(cancel_requested_at, now()),
            refund_requested_at = CASE WHEN status = 'captured' THEN COALESCE(refund_requested_at, now()) ELSE refund_requested_at END,
            next_try_at = now(), attempts = 0, updated_at = now()
        WHERE order_id = NEW.id AND status IN ('held', 'captured');
    END IF;
    RETURN NEW;
END $f$;
DROP TRIGGER IF EXISTS card_holds_on_order_cancel ON orders;
CREATE TRIGGER card_holds_on_order_cancel AFTER UPDATE OF status ON orders
    FOR EACH ROW EXECUTE FUNCTION card_holds_on_order_cancel();
-- +goose StatementEnd

-- +goose Down
-- Nothing to undo: 00043's down drops these functions and the trigger.
SELECT 1;
