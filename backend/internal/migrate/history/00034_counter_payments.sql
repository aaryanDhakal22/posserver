-- +goose Up
-- +goose StatementBegin
-- TERMINAL-PLAN Stage 0d: counter orders arrive from the Pi with their
-- payments (floor events order.placed / order.payment). A payment keeps the
-- tip taken with it and the staff member who took it.
ALTER TABLE payments
    ADD COLUMN tip_cents bigint NOT NULL DEFAULT 0,
    ADD COLUMN staff_id  uuid;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE payments DROP COLUMN staff_id, DROP COLUMN tip_cents;
-- +goose StatementEnd
