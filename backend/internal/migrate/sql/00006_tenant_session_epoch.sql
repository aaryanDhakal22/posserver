-- Manager sessions end when the store's password is rotated (security review
-- M8, 2026-11-12): the session cookie's signed payload carries the store's
-- session epoch, SetTenantCredentials adds one, and a cookie with an older
-- epoch is refused. 0 = never rotated since this column (cookies issued
-- before it carry no epoch and count as 0, so nobody is logged out by the
-- deploy).

-- +goose Up
ALTER TABLE public.tenants ADD COLUMN session_epoch bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE public.tenants DROP COLUMN session_epoch;
