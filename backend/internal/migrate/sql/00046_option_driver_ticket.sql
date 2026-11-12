-- +goose Up
-- +goose StatementBegin
-- "Show on driver ticket" (the owner, 2026-11-12): SoftTouch rang drinks,
-- desserts and sides as 1¢ choices only so the driver saw them on the
-- delivery ticket. Such a choice is $0 now and carries this flag instead;
-- the Pi prints flagged choices on the driver's copy (notes like "Well Done"
-- stay off it). The menu_version trigger on modifier_options (00018) bumps
-- the version on the update, so the Pi pulls the counter menu again.
ALTER TABLE modifier_options ADD COLUMN show_on_driver_ticket boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE modifier_options DROP COLUMN IF EXISTS show_on_driver_ticket;
-- +goose StatementEnd
