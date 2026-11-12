-- +goose Up
-- +goose StatementBegin
-- The owner's defaults for every store (2026-11-12: "everything here is
-- default, not just for one store"). staff.DefaultSettings now has a cashier
-- discount limit of 20 % (was 10) and the employee meal at 20 % (was 50);
-- auto-lock 5 min and the cashier price drop 20 % were already the defaults.
--
-- A store that never saved its terminal settings reads the defaults at run
-- time and needs nothing. A saved row (terminal_settings.settings holds every
-- field once Store › Staff saved it) that still holds an old default value
-- moves to the new one; a value the store changed stays. The version goes up
-- on every row so the Pi's next terminal-config pull (ETag "t<version>") sees
-- a change: the defaults moved for the '{}' rows too.
UPDATE terminal_settings
SET settings = CASE WHEN settings->'cashier_discount_pct' = '10'::jsonb
                    THEN jsonb_set(settings, '{cashier_discount_pct}', '20'::jsonb) ELSE settings END,
    version = version + 1,
    updated_at = now();
UPDATE terminal_settings
SET settings = jsonb_set(settings, '{employee_meal_pct}', '20'::jsonb)
WHERE settings->'employee_meal_pct' = '50'::jsonb;

-- The delivery fee: $4 = store $1 + driver $3 for every store. Migration
-- 00037 saved the old $0 defaults for stores that had no pricing row; a
-- store row that is still exactly that saved default (nobody changed any of
-- its pricing since) now gets the $4 fee with the driver's $3. A store whose
-- pricing was edited keeps its fee.
UPDATE pricing_settings
SET settings = jsonb_set(jsonb_set(settings, '{fees,delivery_fee_cents}', '400'::jsonb),
                         '{fees,delivery_driver_cents}', '300'::jsonb)
WHERE kind = ''
  AND settings = '{"tax_bps":600,"stacking":"best","fees":{"service_fee_cents":0,"service_fee_bps":0,"delivery_fee_cents":0,"free_delivery_over_cents":null,"delivery_min_cents":0},"tips":{"enabled":false,"presets":[15,18,20],"default_pct":18},"card_fee":{"mode":"none","bps":0},"half_topping_price":"full","half_specialty_off_pct":0}'::jsonb;
-- +goose StatementEnd

-- +goose Down
-- Nothing to undo: the rows are ordinary saved settings.
SELECT 1;
