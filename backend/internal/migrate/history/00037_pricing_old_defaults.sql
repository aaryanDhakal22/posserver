-- +goose Up
-- +goose StatementBegin
-- A new store's delivery fee defaults to $4 ($3 driver + $1 store, the
-- owner 2026-11-11: pricing.DefaultStore). A store that never saved its
-- pricing read the defaults at run time, so changing them would raise its
-- delivery fee from $0 without anyone saving anything. Every location
-- without a saved row gets the old defaults saved now (the store row + the
-- website's tips override, exactly as DefaultStore / DefaultOverrides were),
-- so existing stores keep what they charge. Stores made after this get $4.
WITH fresh AS (
    SELECT l.id, l.tenant_id FROM locations l
    WHERE NOT EXISTS (SELECT 1 FROM pricing_settings p WHERE p.location_id = l.id AND p.kind = '')
), store_rows AS (
    INSERT INTO pricing_settings (location_id, tenant_id, kind, settings)
    SELECT id, tenant_id, '',
           '{"tax_bps":600,"stacking":"best","fees":{"service_fee_cents":0,"service_fee_bps":0,"delivery_fee_cents":0,"free_delivery_over_cents":null,"delivery_min_cents":0},"tips":{"enabled":false,"presets":[15,18,20],"default_pct":18},"card_fee":{"mode":"none","bps":0},"half_topping_price":"full","half_specialty_off_pct":0}'::jsonb
    FROM fresh
)
INSERT INTO pricing_settings (location_id, tenant_id, kind, settings)
SELECT id, tenant_id, 'web', '{"fees":null,"tips":{"enabled":true,"presets":[15,18,20],"default_pct":18},"card_fee":null}'::jsonb
FROM fresh
WHERE NOT EXISTS (SELECT 1 FROM pricing_settings p WHERE p.location_id = fresh.id AND p.kind = 'web');
-- +goose StatementEnd

-- +goose Down
-- Nothing to undo: the rows are ordinary saved settings.
SELECT 1;
