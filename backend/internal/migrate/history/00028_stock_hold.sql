-- +goose Up
-- +goose StatementBegin
-- weborder stage 3: an item or topping marked sold out on Quibase "until
-- turned back on" (out_hold) is skipped by the daily restock. Setting it in
-- stock again (anywhere) clears the hold.
ALTER TABLE menu_items ADD COLUMN out_hold boolean NOT NULL DEFAULT false;
ALTER TABLE modifier_options ADD COLUMN out_hold boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE modifier_options DROP COLUMN IF EXISTS out_hold;
ALTER TABLE menu_items DROP COLUMN IF EXISTS out_hold;
-- +goose StatementEnd
