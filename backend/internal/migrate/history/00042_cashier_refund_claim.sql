-- +goose Up
-- +goose StatementBegin
-- The owner's permissions of 2026-11-12 (TERMINAL-PLAN "Permissions"):
--
-- 1. A cashier refunds cash up to $20 per order without a manager (X, the
--    store setting cashier_refund_cents; card refunds always need a
--    manager on the Pi). staff.DefaultSettings had 0 ("every refund is a
--    manager's"); a saved row still on 0 moves to 2000. A store that never
--    saved reads the new default at run time.
-- 2. Drivers claim a delivery nobody took once it is past the order's
--    promised time on the Pi (not after a fixed wait): the terminal setting
--    driver_claim_after_minutes is retired and leaves every saved row.
--
-- The version goes up on every row so the Pi's next terminal-config pull
-- (ETag "t<version>") sees the change.
UPDATE terminal_settings
SET settings = (CASE WHEN settings->'cashier_refund_cents' = '0'::jsonb
                     THEN jsonb_set(settings, '{cashier_refund_cents}', '2000'::jsonb) ELSE settings END)
               - 'driver_claim_after_minutes',
    version = version + 1,
    updated_at = now();
-- +goose StatementEnd

-- +goose Down
-- Nothing to undo: the rows are ordinary saved settings (a store that wants
-- 0 again sets it on Store > Staff).
SELECT 1;
