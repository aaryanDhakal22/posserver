-- +goose Up
-- +goose StatementBegin
-- weborder stage 2: customer accounts + email receipts. An account belongs to
-- one store and is keyed by its verified phone (Twilio Verify text / call
-- code). Sessions are hashed tokens (qcs_…, 90 days, renewed on use). Saved
-- cards live on Square: only the card's id, brand and last 4 are kept here.
-- A channel order may name its account; a guest web order is joined to the
-- account the first time its phone signs in. email_receipts is the durable
-- queue of receipts to send (one per order and kind).
CREATE TABLE customers (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid        NOT NULL,
    location_id        uuid        NOT NULL,
    phone              text        NOT NULL,
    name               text        NOT NULL DEFAULT '',
    email              text        NOT NULL DEFAULT '',
    square_customer_id text        NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    last_seen_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (location_id, phone),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);

CREATE TABLE customer_addresses (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id uuid        NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    label       text        NOT NULL DEFAULT '',
    line1       text        NOT NULL,
    line2       text        NOT NULL DEFAULT '',
    city        text        NOT NULL DEFAULT '',
    state       text        NOT NULL DEFAULT '',
    zip         text        NOT NULL DEFAULT '',
    notes       text        NOT NULL DEFAULT '',
    is_default  boolean     NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_customer_addresses_customer ON customer_addresses (customer_id);
CREATE UNIQUE INDEX idx_customer_addresses_default ON customer_addresses (customer_id) WHERE is_default;

CREATE TABLE customer_sessions (
    token_hash   text        PRIMARY KEY,
    customer_id  uuid        NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    location_id  uuid        NOT NULL,
    expires_at   timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_customer_sessions_customer ON customer_sessions (customer_id);
CREATE INDEX idx_customer_sessions_expires ON customer_sessions (expires_at);

CREATE TABLE customer_cards (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id uuid        NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    gateway     text        NOT NULL,
    card_ref    text        NOT NULL,
    brand       text        NOT NULL DEFAULT '',
    last4       text        NOT NULL DEFAULT '',
    exp_month   integer     NOT NULL DEFAULT 0,
    exp_year    integer     NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (customer_id, card_ref)
);

ALTER TABLE channel_orders ADD COLUMN customer_id uuid REFERENCES customers(id) ON DELETE SET NULL;
CREATE INDEX idx_channel_orders_customer ON channel_orders (customer_id, created_at DESC) WHERE customer_id IS NOT NULL;

-- The store's email: receipts' Reply-To ("" = no replies).
ALTER TABLE site_settings ADD COLUMN reply_email text NOT NULL DEFAULT '';

CREATE TABLE email_receipts (
    order_id    uuid        NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    kind        text        NOT NULL CHECK (kind IN ('placed', 'final')),
    location_id uuid        NOT NULL,
    status      text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed', 'skipped')),
    attempts    integer     NOT NULL DEFAULT 0,
    next_try_at timestamptz NOT NULL DEFAULT now(),
    last_error  text        NOT NULL DEFAULT '',
    message_id  text        NOT NULL DEFAULT '',
    sent_at     timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (order_id, kind)
);
CREATE INDEX idx_email_receipts_work ON email_receipts (next_try_at) WHERE status = 'pending';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS email_receipts;
ALTER TABLE site_settings DROP COLUMN IF EXISTS reply_email;
ALTER TABLE channel_orders DROP COLUMN IF EXISTS customer_id;
DROP TABLE IF EXISTS customer_cards;
DROP TABLE IF EXISTS customer_sessions;
DROP TABLE IF EXISTS customer_addresses;
DROP TABLE IF EXISTS customers;
-- +goose StatementEnd
