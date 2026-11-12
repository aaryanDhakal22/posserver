-- A printer's print width (2026-11-12): how many font-A characters fit on its
-- line. Every Lombardi's printer takes the same 80 mm roll, but the Online
-- printer's head prints a narrower area (48-character lines wrapped), so the
-- Pi lays every slip out for the printer it lands on: 48 = 576 dots (80 mm),
-- 42 = 512 dots (a 72 mm print area), 32 = 384 dots (58 mm). Set in Store ›
-- Printers or by the Pi's width test (RPC printer.width).

-- +goose Up
ALTER TABLE public.printers ADD COLUMN print_columns smallint NOT NULL DEFAULT 48 CONSTRAINT printers_print_columns_check CHECK (print_columns IN (32, 42, 48));

-- +goose Down
ALTER TABLE public.printers DROP COLUMN print_columns;
