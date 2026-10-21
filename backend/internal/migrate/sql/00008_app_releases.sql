-- +goose Up
-- +goose StatementBegin
-- Quibase (counter tablet app) releases, uploaded by the console and fetched
-- by each agent, which serves them to its paired tablets over the LAN. Stored
-- in Postgres (not a volume) so both blue-green slots see them and backups
-- include them; only the newest few per app are kept.
CREATE TABLE app_releases (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    app          text        NOT NULL,
    version_code bigint      NOT NULL,
    version_name text        NOT NULL,
    sha256       text        NOT NULL,
    size         bigint      NOT NULL,
    apk          bytea       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (app, version_code)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS app_releases;
-- +goose StatementEnd
