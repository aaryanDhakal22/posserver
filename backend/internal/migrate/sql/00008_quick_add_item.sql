-- Quick add (owner, 2026-11-13): the terminal's Quicc Add line (the cashier
-- types a name + price for something not on the menu) prints where the
-- store's "Quick add" menu item routes, like any item: its own printer, else
-- its category's default, "no kitchen ticket" = receipt only; neither = every
-- station (the old behaviour). One such item per location, made by main the
-- first time the Pi pulls the counter menu or Store › Menu opens (its own
-- "Quick add" menu + category). It is never sold from any menu: no view
-- build, no name routing for online orders, no sold-out list.

-- +goose Up
ALTER TABLE public.menu_items ADD COLUMN quick_add boolean DEFAULT false NOT NULL;
CREATE UNIQUE INDEX menu_items_quick_add_uq ON public.menu_items USING btree (location_id) WHERE quick_add;

-- +goose Down
DROP INDEX IF EXISTS public.menu_items_quick_add_uq;
ALTER TABLE public.menu_items DROP COLUMN IF EXISTS quick_add;
