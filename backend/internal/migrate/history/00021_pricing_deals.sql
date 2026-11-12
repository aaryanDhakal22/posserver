-- +goose Up
-- +goose StatementBegin
-- main 3.0 step 4: pricing. pricing_settings holds a location's defaults
-- (kind '') — tax, stacking, fees, tips, card fee — and, per channel kind,
-- whole sets that replace the store's (fees / tips / card fee). deals are
-- the store's, each switched on for some channel kinds; def holds the
-- kind's fields (eligibility by bank item / category / size ids, schedule,
-- fulfil). One deal per code per location (case-insensitive).
CREATE TABLE pricing_settings (
    location_id uuid        NOT NULL,
    tenant_id   uuid        NOT NULL,
    kind        text        NOT NULL DEFAULT '' CHECK (kind IN ('', 'web', 'operator', 'counter', 'vendor')),
    settings    jsonb       NOT NULL DEFAULT '{}'::jsonb,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (location_id, kind),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);

CREATE TABLE deals (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid        NOT NULL,
    location_id   uuid        NOT NULL,
    kind          text        NOT NULL CHECK (kind IN ('bundle', 'mix', 'nth', 'code', 'free_item')),
    name          text        NOT NULL,
    description   text        NOT NULL DEFAULT '',
    active        boolean     NOT NULL DEFAULT true,
    channel_kinds text[]      NOT NULL DEFAULT '{web,operator,counter}',
    def           jsonb       NOT NULL DEFAULT '{}'::jsonb,
    code          text        NOT NULL DEFAULT '',
    position      integer     NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_deals_location ON deals (location_id);
CREATE UNIQUE INDEX idx_deals_code ON deals (location_id, upper(code)) WHERE kind = 'code';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS deals;
DROP TABLE IF EXISTS pricing_settings;
-- +goose StatementEnd
