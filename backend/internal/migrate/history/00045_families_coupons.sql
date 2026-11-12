-- +goose Up
-- +goose StatementBegin
-- Families + coupons (the owner's Centurion pass, 2026-11-12). A family is a
-- named group of a store's bank items (kind 'item': Can Soda, 8" Hot Sub, …)
-- or modifier options (kind 'mod': 14" Pizza Toppings, …); a mod family has a
-- class: regular (a coupon may make it free), premium or unpriced (never).
-- An item / option without a family is "no family yet". Coupons are deals of
-- kind 'coupon' whose parts name item families (deals.def).
CREATE TABLE menu_families (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    kind        text        NOT NULL CHECK (kind IN ('item', 'mod')),
    name        text        NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 60),
    class       text        NOT NULL DEFAULT '' CHECK (class IN ('', 'regular', 'premium', 'unpriced')),
    position    integer     NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE,
    CHECK ((kind = 'item' AND class = '') OR (kind = 'mod' AND class <> ''))
);
CREATE INDEX idx_menu_families_location ON menu_families (location_id);
CREATE UNIQUE INDEX idx_menu_families_name ON menu_families (location_id, kind, lower(btrim(name)));

ALTER TABLE menu_items       ADD COLUMN family_id uuid REFERENCES menu_families(id) ON DELETE SET NULL;
ALTER TABLE modifier_options ADD COLUMN family_id uuid REFERENCES menu_families(id) ON DELETE SET NULL;
CREATE INDEX idx_menu_items_family ON menu_items (family_id) WHERE family_id IS NOT NULL;
CREATE INDEX idx_modifier_options_family ON modifier_options (family_id) WHERE family_id IS NOT NULL;

ALTER TABLE deals DROP CONSTRAINT IF EXISTS deals_kind_check;
ALTER TABLE deals ADD CONSTRAINT deals_kind_check CHECK (kind IN ('bundle', 'mix', 'nth', 'code', 'free_item', 'coupon'));

-- The Pi pulls the counter menu (with the counter's deals and pricing) when
-- the menu version moves: families, deals and pricing settings bump it too.
CREATE TRIGGER trg_menu_version AFTER INSERT OR UPDATE OR DELETE ON menu_families FOR EACH ROW EXECUTE FUNCTION public.bump_menu_version();
CREATE TRIGGER trg_menu_version AFTER INSERT OR UPDATE OR DELETE ON deals FOR EACH ROW EXECUTE FUNCTION public.bump_menu_version();
CREATE TRIGGER trg_menu_version AFTER INSERT OR UPDATE OR DELETE ON pricing_settings FOR EACH ROW EXECUTE FUNCTION public.bump_menu_version();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_menu_version ON pricing_settings;
DROP TRIGGER IF EXISTS trg_menu_version ON deals;
DROP TRIGGER IF EXISTS trg_menu_version ON menu_families;
DELETE FROM deals WHERE kind = 'coupon';
ALTER TABLE deals DROP CONSTRAINT IF EXISTS deals_kind_check;
ALTER TABLE deals ADD CONSTRAINT deals_kind_check CHECK (kind IN ('bundle', 'mix', 'nth', 'code', 'free_item'));
ALTER TABLE modifier_options DROP COLUMN IF EXISTS family_id;
ALTER TABLE menu_items       DROP COLUMN IF EXISTS family_id;
DROP TABLE IF EXISTS menu_families;
-- +goose StatementEnd
