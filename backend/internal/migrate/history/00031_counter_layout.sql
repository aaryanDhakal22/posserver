-- +goose Up
-- +goose StatementBegin
-- TERMINAL-PLAN.md Stage 0a: the counter screen's layout (tab strip, button
-- grids with pages, modifier screens, per-item modifier flows, function
-- buttons) as one JSON document on a counter view. NULL = no layout. Only a
-- counter view may hold one. menu_views' row trigger (00018) bumps the menu
-- version on every update, so a layout change is a new menu version.
ALTER TABLE menu_views ADD COLUMN layout jsonb;
ALTER TABLE menu_views ADD CONSTRAINT menu_views_layout_counter
    CHECK (layout IS NULL OR (kind = 'counter' AND jsonb_typeof(layout) = 'object'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE menu_views DROP CONSTRAINT IF EXISTS menu_views_layout_counter;
ALTER TABLE menu_views DROP COLUMN IF EXISTS layout;
-- +goose StatementEnd
