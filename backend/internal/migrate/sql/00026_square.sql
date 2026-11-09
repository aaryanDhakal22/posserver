-- +goose Up
-- +goose StatementBegin
-- weborder stage 2: Square. A store connects its Square account (OAuth);
-- the tokens are kept encrypted (AES-GCM, SQUARE_TOKEN_KEY). An online card
-- order is a hold (authorized, not charged): card_holds tracks it until it is
-- captured — at the driver's clock-out, at Close batch, or by main 24 h after
-- the order — or cancelled with the order. A tip added after the order rides
-- on the hold when Square lets it grow, else it is a second charge on the
-- card saved for that order. No card data is stored: only Square's ids.
CREATE TABLE square_connections (
    location_id        uuid        PRIMARY KEY,
    tenant_id          uuid        NOT NULL,
    merchant_id        text        NOT NULL,
    square_location_id text        NOT NULL DEFAULT '',
    access_token_enc   bytea       NOT NULL,
    refresh_token_enc  bytea       NOT NULL,
    expires_at         timestamptz NOT NULL,
    refreshed_at       timestamptz NOT NULL DEFAULT now(),
    status             text        NOT NULL DEFAULT 'connected' CHECK (status IN ('connected', 'revoked', 'error')),
    last_error         text        NOT NULL DEFAULT '',
    connected_at       timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_square_connections_merchant ON square_connections (merchant_id);

CREATE TABLE card_holds (
    order_id             uuid        PRIMARY KEY REFERENCES orders(id) ON DELETE CASCADE,
    tenant_id            uuid        NOT NULL,
    location_id          uuid        NOT NULL,
    gateway              text        NOT NULL,
    payment_ref          text        NOT NULL,
    amount_cents         bigint      NOT NULL,
    tip_cents            bigint      NOT NULL DEFAULT 0,
    added_tip_cents      bigint      NOT NULL DEFAULT 0,
    status               text        NOT NULL DEFAULT 'held' CHECK (status IN ('held', 'captured', 'cancelled', 'failed')),
    capture_reason       text        NOT NULL DEFAULT '',
    due_at               timestamptz NOT NULL,
    capture_requested_at timestamptz,
    next_try_at          timestamptz NOT NULL DEFAULT now(),
    attempts             integer     NOT NULL DEFAULT 0,
    last_error           text        NOT NULL DEFAULT '',
    captured_cents       bigint      NOT NULL DEFAULT 0,
    tip_payment_ref      text        NOT NULL DEFAULT '',
    tip_note             text        NOT NULL DEFAULT '',
    captured_at          timestamptz,
    cancelled_at         timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_card_holds_work ON card_holds (next_try_at) WHERE status = 'held';
CREATE INDEX idx_card_holds_location ON card_holds (location_id, created_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS card_holds;
DROP TABLE IF EXISTS square_connections;
-- +goose StatementEnd
