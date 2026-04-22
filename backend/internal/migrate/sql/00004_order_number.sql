-- +goose Up

ALTER TABLE orders ADD COLUMN OrderNumber INTEGER;

CREATE TABLE IF NOT EXISTS order_number_counter (
    id    INTEGER PRIMARY KEY DEFAULT 1,
    value INTEGER NOT NULL DEFAULT 1099,
    CONSTRAINT single_row CHECK (id = 1)
);

INSERT INTO order_number_counter(id, value) VALUES (1, 1099) ON CONFLICT DO NOTHING;

-- +goose Down

ALTER TABLE orders DROP COLUMN IF EXISTS OrderNumber;
DROP TABLE IF EXISTS order_number_counter;
