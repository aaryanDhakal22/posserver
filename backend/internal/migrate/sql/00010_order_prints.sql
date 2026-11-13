-- Store › Orders' order panel (the owner, 2026-11-13; agent contract
-- §2.10ag, main §2.5zn):
--   * order_prints: what each print job of an order actually printed, per
--     ticket and printer, as the Pi reports it (floor event order.printed):
--     append only, one row per event (idempotent on its id), gone with the
--     order;
--   * payments.card_brand: a card payment's brand as Square named it on the
--     terminal / the Pi / the phone (amex | discover | mastercard | visa |
--     other; '' = not sent: an older Pi, or not a card).

-- +goose Up
ALTER TABLE public.payments ADD COLUMN card_brand text DEFAULT ''::text NOT NULL;

CREATE TABLE public.order_prints (
    event_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    location_id uuid NOT NULL,
    order_id uuid NOT NULL,
    mode text NOT NULL,
    tickets jsonb NOT NULL,
    at timestamp with time zone NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT order_prints_pkey PRIMARY KEY (event_id),
    CONSTRAINT order_prints_order_id_fkey FOREIGN KEY (order_id) REFERENCES public.orders(id) ON DELETE CASCADE
);
CREATE INDEX idx_order_prints_order ON public.order_prints USING btree (order_id, at);

-- +goose Down
DROP TABLE public.order_prints;
ALTER TABLE public.payments DROP COLUMN IF EXISTS card_brand;
