-- +goose Up
-- +goose StatementBegin
-- order.geo "confirmed" (main 2.1.0 review fix, 2026-10-25).
--
-- confirmed: the pending result comes straight from staff — geo.pin or
--   geo.choose — so its order.geo push carries "confirmed": true (the agent
--   then emits address.confirmed). Set by the pin / choose resets, cleared by
--   every other reset (re-locate, store moved) and once the push is acked, so a
--   cache hit on a pinned address or any later re-push never claims it.
ALTER TABLE order_geo ADD COLUMN confirmed boolean NOT NULL DEFAULT false;
ALTER TABLE geo_external_stops ADD COLUMN confirmed boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE geo_external_stops DROP COLUMN IF EXISTS confirmed;
ALTER TABLE order_geo DROP COLUMN IF EXISTS confirmed;
-- +goose StatementEnd
