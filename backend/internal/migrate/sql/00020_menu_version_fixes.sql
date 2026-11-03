-- +goose Up
-- +goose StatementBegin
-- main 3.0 fixes to 00018's menu version trigger:
-- * Deleting a location cascades into its menu rows; their AFTER triggers
--   run once the location row is already gone, and the bump's insert into
--   menu_versions then broke the delete (FK). The bump now skips a location
--   that no longer exists.
-- * A channel moved to another view reads a different menu: the move bumps
--   the location's version, so the channel's ETag / version poll change.
CREATE OR REPLACE FUNCTION bump_menu_version() RETURNS trigger AS $$
DECLARE
    loc uuid;
BEGIN
    IF TG_TABLE_NAME = 'item_modifier_groups' THEN
        SELECT location_id INTO loc FROM menu_items WHERE id = COALESCE(NEW.item_id, OLD.item_id);
    ELSIF TG_OP = 'DELETE' THEN
        loc := OLD.location_id;
    ELSE
        loc := NEW.location_id;
    END IF;
    IF loc IS NOT NULL AND EXISTS (SELECT 1 FROM locations WHERE id = loc) THEN
        INSERT INTO menu_versions (location_id, version, changed_at) VALUES (loc, 2, now())
        ON CONFLICT (location_id) DO UPDATE SET version = menu_versions.version + 1, changed_at = now();
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_menu_version_channel_view AFTER UPDATE OF view_id ON channels
    FOR EACH ROW WHEN (OLD.view_id IS DISTINCT FROM NEW.view_id) EXECUTE FUNCTION bump_menu_version();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_menu_version_channel_view ON channels;
CREATE OR REPLACE FUNCTION bump_menu_version() RETURNS trigger AS $$
DECLARE
    loc uuid;
BEGIN
    IF TG_TABLE_NAME = 'item_modifier_groups' THEN
        SELECT location_id INTO loc FROM menu_items WHERE id = COALESCE(NEW.item_id, OLD.item_id);
    ELSIF TG_OP = 'DELETE' THEN
        loc := OLD.location_id;
    ELSE
        loc := NEW.location_id;
    END IF;
    IF loc IS NOT NULL THEN
        INSERT INTO menu_versions (location_id, version, changed_at) VALUES (loc, 2, now())
        ON CONFLICT (location_id) DO UPDATE SET version = menu_versions.version + 1, changed_at = now();
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd
