-- Included toppings (owner, 2026-11-13): an item's price may include N
-- toppings (item attribute included_toppings, per size
-- included_toppings_sizes; no column). An option marked never_included is
-- always charged, even on such an item (SoftTouch's "always charge": Shrimp on
-- a build-your-own pizza).

-- +goose Up
ALTER TABLE public.modifier_options ADD COLUMN never_included boolean DEFAULT false NOT NULL;

-- +goose Down
ALTER TABLE public.modifier_options DROP COLUMN IF EXISTS never_included;
