-- +goose Up
-- +goose StatementBegin
-- main 3.0: the Canonical Menu becomes a BANK — the strict, finest-grained,
-- priced menu of a location — and each sales channel reads a VIEW of it
-- (PORTS-PLAN.md, WEBORDER-PLAN.md). Today's menu tables are the bank; this
-- adds what they lacked, the views, their compiled builds and a per-location
-- menu version every change bumps.

-- Store-wide sizes ("10 Small", "12 Medium", …): an item comes in some of them.
CREATE TABLE menu_sizes (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    name        text        NOT NULL,
    short       text        NOT NULL DEFAULT '',
    position    integer     NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE,
    UNIQUE (id, tenant_id)
);
CREATE UNIQUE INDEX idx_menu_sizes_name ON menu_sizes (location_id, lower(name));

-- An item's sizes and their prices (an item with none keeps price_cents).
CREATE TABLE menu_item_sizes (
    tenant_id    uuid    NOT NULL,
    location_id  uuid    NOT NULL,
    item_id      uuid    NOT NULL,
    size_id      uuid    NOT NULL,
    price_cents  bigint  NOT NULL CHECK (price_cents >= 0),
    kitchen_name text    NOT NULL DEFAULT '',
    position     integer NOT NULL DEFAULT 0,
    FOREIGN KEY (item_id, tenant_id) REFERENCES menu_items(id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (size_id, tenant_id) REFERENCES menu_sizes(id, tenant_id) ON DELETE CASCADE,
    PRIMARY KEY (item_id, size_id)
);
CREATE INDEX idx_menu_item_sizes_size ON menu_item_sizes (size_id);

-- A modifier option's price by the item's size (a topping costs more on a 16").
CREATE TABLE modifier_option_prices (
    tenant_id   uuid   NOT NULL,
    location_id uuid   NOT NULL,
    option_id   uuid   NOT NULL,
    size_id     uuid   NOT NULL,
    price_cents bigint NOT NULL CHECK (price_cents >= 0),
    FOREIGN KEY (option_id, tenant_id) REFERENCES modifier_options(id, tenant_id) ON DELETE CASCADE,
    FOREIGN KEY (size_id, tenant_id) REFERENCES menu_sizes(id, tenant_id) ON DELETE CASCADE,
    PRIMARY KEY (option_id, size_id)
);
CREATE INDEX idx_modifier_option_prices_size ON modifier_option_prices (size_id);

-- kitchen_name: the short name on kitchen tickets ('' = the item name);
-- sku: the store's own code. portions: the group's options can go on a half.
-- is_default: pre-selected and removable (a ticket prints "NO x" when removed).
ALTER TABLE menu_items ADD COLUMN kitchen_name text NOT NULL DEFAULT '';
ALTER TABLE menu_items ADD COLUMN sku text NOT NULL DEFAULT '';
ALTER TABLE modifier_groups ADD COLUMN portions boolean NOT NULL DEFAULT false;
ALTER TABLE modifier_options ADD COLUMN is_default boolean NOT NULL DEFAULT false;
ALTER TABLE modifier_options ADD COLUMN kitchen_name text NOT NULL DEFAULT '';

-- A view of the bank for one kind of channel: web, operator (call center),
-- counter, vendor (a vendor's names mapped to the bank, e.g. Brygid).
-- rules: {markup_bps, round, hidden_categories, …}.
CREATE TABLE menu_views (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    kind        text        NOT NULL CHECK (kind IN ('web', 'operator', 'counter', 'vendor')),
    name        text        NOT NULL,
    rules       jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE,
    UNIQUE (id, tenant_id)
);
CREATE INDEX idx_menu_views_location ON menu_views (location_id);

-- One change a view makes to the bank, always anchored to bank ids:
--   item     rename / describe / photo / hide / move / reprice one bank item
--   merge    one view item that is a choice between several bank items
--   augment  an extra view item made of bank items (a combo, a bundle)
--   map      (vendor) a vendor's item name [+ size] → a bank item [+ size]
CREATE TABLE menu_view_entries (
    id                   uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            uuid        NOT NULL,
    location_id          uuid        NOT NULL,
    view_id              uuid        NOT NULL,
    kind                 text        NOT NULL CHECK (kind IN ('item', 'merge', 'augment', 'map')),
    anchor               jsonb       NOT NULL DEFAULT '{}'::jsonb,
    display_name         text        NOT NULL DEFAULT '',
    description          text        NOT NULL DEFAULT '',
    photo                text        NOT NULL DEFAULT '',
    category             text        NOT NULL DEFAULT '',
    position             integer     NOT NULL DEFAULT 0,
    hidden               boolean     NOT NULL DEFAULT false,
    price_override_cents bigint,
    extra                jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (view_id, tenant_id) REFERENCES menu_views(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_menu_view_entries_view ON menu_view_entries (view_id);

-- A view compiled against the bank at one menu version (the cache channels read).
CREATE TABLE menu_view_builds (
    view_id      uuid        PRIMARY KEY REFERENCES menu_views(id) ON DELETE CASCADE,
    menu_version bigint      NOT NULL,
    build        jsonb       NOT NULL,
    built_at     timestamptz NOT NULL DEFAULT now()
);

-- A channel may read a chosen view (NULL = its location's default view of its kind).
ALTER TABLE channels ADD COLUMN view_id uuid REFERENCES menu_views(id) ON DELETE SET NULL;

-- Every change to a location's bank or views bumps its menu version, so a
-- cached build (and every adapter caching the menu) knows to refresh.
CREATE TABLE menu_versions (
    location_id uuid        PRIMARY KEY REFERENCES locations(id) ON DELETE CASCADE,
    version     bigint      NOT NULL DEFAULT 1,
    changed_at  timestamptz NOT NULL DEFAULT now()
);

CREATE FUNCTION bump_menu_version() RETURNS trigger AS $$
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

CREATE TRIGGER trg_menu_version AFTER INSERT OR UPDATE OR DELETE ON menus FOR EACH ROW EXECUTE FUNCTION bump_menu_version();
CREATE TRIGGER trg_menu_version AFTER INSERT OR UPDATE OR DELETE ON menu_categories FOR EACH ROW EXECUTE FUNCTION bump_menu_version();
CREATE TRIGGER trg_menu_version AFTER INSERT OR UPDATE OR DELETE ON menu_items FOR EACH ROW EXECUTE FUNCTION bump_menu_version();
CREATE TRIGGER trg_menu_version AFTER INSERT OR UPDATE OR DELETE ON modifier_groups FOR EACH ROW EXECUTE FUNCTION bump_menu_version();
CREATE TRIGGER trg_menu_version AFTER INSERT OR UPDATE OR DELETE ON modifier_options FOR EACH ROW EXECUTE FUNCTION bump_menu_version();
CREATE TRIGGER trg_menu_version AFTER INSERT OR UPDATE OR DELETE ON item_modifier_groups FOR EACH ROW EXECUTE FUNCTION bump_menu_version();
CREATE TRIGGER trg_menu_version AFTER INSERT OR UPDATE OR DELETE ON menu_sizes FOR EACH ROW EXECUTE FUNCTION bump_menu_version();
CREATE TRIGGER trg_menu_version AFTER INSERT OR UPDATE OR DELETE ON menu_item_sizes FOR EACH ROW EXECUTE FUNCTION bump_menu_version();
CREATE TRIGGER trg_menu_version AFTER INSERT OR UPDATE OR DELETE ON modifier_option_prices FOR EACH ROW EXECUTE FUNCTION bump_menu_version();
CREATE TRIGGER trg_menu_version AFTER INSERT OR UPDATE OR DELETE ON menu_views FOR EACH ROW EXECUTE FUNCTION bump_menu_version();
CREATE TRIGGER trg_menu_version AFTER INSERT OR UPDATE OR DELETE ON menu_view_entries FOR EACH ROW EXECUTE FUNCTION bump_menu_version();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_menu_version ON menu_view_entries;
DROP TRIGGER IF EXISTS trg_menu_version ON menu_views;
DROP TRIGGER IF EXISTS trg_menu_version ON modifier_option_prices;
DROP TRIGGER IF EXISTS trg_menu_version ON menu_item_sizes;
DROP TRIGGER IF EXISTS trg_menu_version ON menu_sizes;
DROP TRIGGER IF EXISTS trg_menu_version ON item_modifier_groups;
DROP TRIGGER IF EXISTS trg_menu_version ON modifier_options;
DROP TRIGGER IF EXISTS trg_menu_version ON modifier_groups;
DROP TRIGGER IF EXISTS trg_menu_version ON menu_items;
DROP TRIGGER IF EXISTS trg_menu_version ON menu_categories;
DROP TRIGGER IF EXISTS trg_menu_version ON menus;
DROP FUNCTION IF EXISTS bump_menu_version();
DROP TABLE IF EXISTS menu_versions;
ALTER TABLE channels DROP COLUMN IF EXISTS view_id;
DROP TABLE IF EXISTS menu_view_builds;
DROP TABLE IF EXISTS menu_view_entries;
DROP TABLE IF EXISTS menu_views;
ALTER TABLE modifier_options DROP COLUMN IF EXISTS kitchen_name;
ALTER TABLE modifier_options DROP COLUMN IF EXISTS is_default;
ALTER TABLE modifier_groups DROP COLUMN IF EXISTS portions;
ALTER TABLE menu_items DROP COLUMN IF EXISTS sku;
ALTER TABLE menu_items DROP COLUMN IF EXISTS kitchen_name;
DROP TABLE IF EXISTS modifier_option_prices;
DROP TABLE IF EXISTS menu_item_sizes;
DROP TABLE IF EXISTS menu_sizes;
-- +goose StatementEnd
