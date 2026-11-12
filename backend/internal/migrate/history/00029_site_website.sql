-- +goose Up
-- +goose StatementBegin
-- weborder: the Website page. The look is edited as a draft (draft_at set
-- while one is open), published by copying it over `published` (version + 1).
-- preview_token opens the draft on the storefront (the private preview
-- link). site_images holds the logo and cover photo (cropped in the browser,
-- small; the storefront serves them through /channel/v1/site/images/{id}).
ALTER TABLE site_settings
    ADD COLUMN draft_at      timestamptz,
    ADD COLUMN published_at  timestamptz,
    ADD COLUMN published_by  text NOT NULL DEFAULT '',
    ADD COLUMN preview_token text NOT NULL DEFAULT '';

CREATE TABLE site_images (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    location_id  uuid        NOT NULL,
    tenant_id    uuid        NOT NULL,
    kind         text        NOT NULL CHECK (kind IN ('logo', 'cover')),
    content_type text        NOT NULL,
    bytes        bytea       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_site_images_location ON site_images (location_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS site_images;
ALTER TABLE site_settings
    DROP COLUMN IF EXISTS preview_token,
    DROP COLUMN IF EXISTS published_by,
    DROP COLUMN IF EXISTS published_at,
    DROP COLUMN IF EXISTS draft_at;
-- +goose StatementEnd
