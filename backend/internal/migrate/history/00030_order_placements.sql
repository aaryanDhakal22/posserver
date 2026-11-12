-- +goose Up
-- +goose StatementBegin
-- DURABILITY-PLAN.md steps 1–3: a channel's order is a placement first. The
-- row is written (insert-or-get on the channel's client_order_id) before any
-- money moves; the work runs detached from the request, and a sweeper finishes
-- or undoes placements whose worker went away. The order itself is created only
-- once its card is held (or it is cash) — in the same transaction that marks
-- the placement placed — so the Pi never sees a pending order.
CREATE TABLE order_placements (
    channel_id         uuid        NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    client_order_id    text        NOT NULL,
    tenant_id          uuid        NOT NULL,
    location_id        uuid        NOT NULL,
    body_hash          text        NOT NULL,
    request            jsonb       NOT NULL,           -- the request without its card token
    card_token         text        NOT NULL DEFAULT '', -- the one-time token, cleared once final
    verification_token text        NOT NULL DEFAULT '',
    customer_id        uuid,                            -- the signed-in customer (X-Customer-Session)
    operator           jsonb,                           -- the operator who took it (operator channels)
    expires_at         timestamptz,
    state              text        NOT NULL DEFAULT 'pending'
                       CHECK (state IN ('pending', 'placed', 'refused', 'expired', 'voided')),
    waiting            text        NOT NULL DEFAULT ''  -- pending, but nobody works on it until the caller comes back:
                       CHECK (waiting IN ('', 'verification', 'retry')), -- 3-D Secure / a temporary failure
    result             jsonb,                           -- the last refusal {status, code, message, issues}
    order_id           uuid,
    worker             text        NOT NULL DEFAULT '', -- the boot id of the main working on it ('' = nobody)
    attempts           integer     NOT NULL DEFAULT 1,
    charge_started     boolean     NOT NULL DEFAULT false, -- the card call was made (the hold may exist)
    charge_cents       bigint      NOT NULL DEFAULT 0,     -- the total the card call asked for
    due_alert_at       timestamptz,                     -- a scheduled order: check the Pi then (due − 30 min)
    due_alerted        boolean     NOT NULL DEFAULT false,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, client_order_id)
);
CREATE INDEX idx_order_placements_pending ON order_placements (updated_at) WHERE state = 'pending';
CREATE INDEX idx_order_placements_due ON order_placements (due_alert_at) WHERE state = 'placed' AND NOT due_alerted;
CREATE INDEX idx_order_placements_age ON order_placements (created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS order_placements;
-- +goose StatementEnd
