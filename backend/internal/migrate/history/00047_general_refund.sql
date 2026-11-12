-- +goose Up
-- +goose StatementBegin
-- General refund (the owner, 2026-11-12; agent contract §2.10x, contract/main.md
-- §2.5zb "refund.general"): cash a terminal gave back with no order. main
-- keeps it as an unlinked counter refund: a payments row with no order_id,
-- origin 'counter', status 'refunded' (a negative amount), the store
-- (location_id) and the Pi's batch, so the Z check (Σ counter payments per
-- method and batch) still matches the Pi's cash. reason = why it was given.
ALTER TABLE payments ALTER COLUMN order_id DROP NOT NULL,
    ADD COLUMN reason text NOT NULL DEFAULT '',
    ADD CONSTRAINT payments_unlinked_ck CHECK (order_id IS NOT NULL
        OR (origin = 'counter' AND status = 'refunded' AND location_id IS NOT NULL));
CREATE INDEX idx_payments_unlinked ON payments (location_id, batch_id) WHERE order_id IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM payments WHERE order_id IS NULL;
DROP INDEX IF EXISTS idx_payments_unlinked;
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_unlinked_ck,
    DROP COLUMN IF EXISTS reason,
    ALTER COLUMN order_id SET NOT NULL;
-- +goose StatementEnd
