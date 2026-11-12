-- +goose Up
-- +goose StatementBegin
-- Coupons re-price their lines (the owner, 2026-11-12; contract/main.md "Families + coupons",
-- agent contract §2.10t): an order line sold in a coupon carries the coupon's name and which
-- applied coupon it is (1 = the order's first; two of the same coupon stay apart), so counter
-- orders the Pi sends and channel orders keep the coupon grouping. '' / 0 = in no coupon
-- (every older line).
ALTER TABLE line_items
    ADD COLUMN coupon    text    NOT NULL DEFAULT '',
    ADD COLUMN coupon_no integer NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE line_items DROP COLUMN coupon_no, DROP COLUMN coupon;
-- +goose StatementEnd
