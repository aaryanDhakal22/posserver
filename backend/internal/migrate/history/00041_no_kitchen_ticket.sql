-- +goose Up
-- +goose StatementBegin
-- "No kitchen ticket" (owner, 2026-11-12): an item that gets no station ticket
-- of its own and is left out of "Also on this order" on the other stations'
-- tickets; it still prints on the customer receipt / delivery ticket (drinks,
-- desserts, fees: "everything else is gathered by the driver").
--
-- Effective for an item = its own flag, or (it has no printer of its own and)
-- its category's flag. Such an item has no printer route: the counter menu's
-- routes say {printer_id: "", no_kitchen: true} and an online / channel line
-- matched to it is stamped routing "none" with no printer_id.
--
-- The menu_version triggers on both tables bump the version, so the Pi pulls
-- the counter menu again after a change.
ALTER TABLE menu_categories ADD COLUMN no_kitchen_ticket boolean NOT NULL DEFAULT false;
ALTER TABLE menu_items      ADD COLUMN no_kitchen_ticket boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE menu_items      DROP COLUMN IF EXISTS no_kitchen_ticket;
ALTER TABLE menu_categories DROP COLUMN IF EXISTS no_kitchen_ticket;
-- +goose StatementEnd
