-- +goose Up
-- +goose StatementBegin

-- Kitchen printing (owner decisions, 2026-10-15).

-- One designated RECEIPT printer per location prints the full customer
-- receipt; every other printer is a kitchen station.
ALTER TABLE printers ADD COLUMN is_receipt boolean NOT NULL DEFAULT false;
CREATE UNIQUE INDEX idx_printers_one_receipt ON printers (location_id) WHERE is_receipt;

-- Per-line routing, stamped by main when the order arrives (the menu matched
-- by item name → the item's printer, else its category's default printer):
--   size          the vendor's size, kept out of `name` so names match the menu
--   menu_item_id  the matched menu item (no FK: history outlives menu edits)
--   printer_id    the station printer the line prints on (no FK, same reason)
--   routing       'matched' | 'unmatched' (no menu item of that name) |
--                 'unassigned' (matched, but no printer set); '' = pre-routing
ALTER TABLE line_items ADD COLUMN size         text NOT NULL DEFAULT '';
ALTER TABLE line_items ADD COLUMN menu_item_id uuid;
ALTER TABLE line_items ADD COLUMN printer_id   uuid;
ALTER TABLE line_items ADD COLUMN routing      text NOT NULL DEFAULT '';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE line_items DROP COLUMN IF EXISTS routing;
ALTER TABLE line_items DROP COLUMN IF EXISTS printer_id;
ALTER TABLE line_items DROP COLUMN IF EXISTS menu_item_id;
ALTER TABLE line_items DROP COLUMN IF EXISTS size;
DROP INDEX IF EXISTS idx_printers_one_receipt;
ALTER TABLE printers DROP COLUMN IF EXISTS is_receipt;
-- +goose StatementEnd
