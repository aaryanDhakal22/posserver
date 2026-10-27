-- +goose Up
-- +goose StatementBegin
-- main 2.2.2: smarter address retry + staff address search.
--
-- corrected: the point was found only by the area retry — the street line
--   without its trailing notes, searched with the state alone inside the
--   store's map box (the POS sends its default town + zip with every
--   address). order.geo carries "corrected": true so the tablet tags it.
-- note: a short staff-facing line on a result, today only a geo.search that
--   found nothing ("no match for that search").
ALTER TABLE order_geo ADD COLUMN corrected boolean NOT NULL DEFAULT false;
ALTER TABLE order_geo ADD COLUMN note text NOT NULL DEFAULT '';
ALTER TABLE geo_external_stops ADD COLUMN corrected boolean NOT NULL DEFAULT false;
ALTER TABLE geo_external_stops ADD COLUMN note text NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE geo_external_stops DROP COLUMN IF EXISTS note;
ALTER TABLE geo_external_stops DROP COLUMN IF EXISTS corrected;
ALTER TABLE order_geo DROP COLUMN IF EXISTS note;
ALTER TABLE order_geo DROP COLUMN IF EXISTS corrected;
-- +goose StatementEnd
