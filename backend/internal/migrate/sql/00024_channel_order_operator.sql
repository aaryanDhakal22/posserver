-- +goose Up
-- +goose StatementBegin
-- main 3.0 step 7: the call-center operator who took a channel order
-- (signed in to the channel API; NULL for every other order, or once the
-- operator is deleted).
ALTER TABLE channel_orders ADD COLUMN operator_id uuid REFERENCES operators(id) ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE channel_orders DROP COLUMN IF EXISTS operator_id;
-- +goose StatementEnd
