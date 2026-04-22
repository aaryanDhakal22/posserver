-- +goose Up

CREATE TABLE IF NOT EXISTS auth_keys (
    id  INTEGER PRIMARY KEY DEFAULT 1,
    key TEXT NOT NULL,
    CONSTRAINT single_row CHECK (id = 1)
);

-- +goose Down

DROP TABLE IF EXISTS auth_keys;
