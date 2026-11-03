-- +goose Up
-- +goose StatementBegin
-- main 3.0 step 6: tracking. A channel order gets an unguessable public
-- tracking token (GET /track/v1/{token}) and alerted_at (the owner was paged
-- that the Pi hasn't accepted it). channel_events is each channel's live
-- stream: one row per change of one of its orders (written by the
-- channel-tracking outbox handler), kept 24 h for Last-Event-ID resumes.
ALTER TABLE channel_orders ADD COLUMN track_token text;
ALTER TABLE channel_orders ADD COLUMN alerted_at timestamptz;
CREATE UNIQUE INDEX idx_channel_orders_track ON channel_orders (track_token) WHERE track_token IS NOT NULL;

CREATE TABLE channel_events (
    id         bigserial   PRIMARY KEY,
    channel_id uuid        NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    order_id   uuid        NOT NULL,
    type       text        NOT NULL,
    payload    jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_channel_events_channel ON channel_events (channel_id, id);
CREATE INDEX idx_channel_events_created ON channel_events (created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS channel_events;
DROP INDEX IF EXISTS idx_channel_orders_track;
ALTER TABLE channel_orders DROP COLUMN IF EXISTS alerted_at;
ALTER TABLE channel_orders DROP COLUMN IF EXISTS track_token;
-- +goose StatementEnd
