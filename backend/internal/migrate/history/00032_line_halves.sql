-- +goose Up
-- +goose StatementBegin
-- TERMINAL-PLAN Stage 0b: half and half on the order line. A modifier child
-- says which part of the pie it is on (portion: '' = whole | first | second)
-- and what it does (mode: add | only | no | specialty; '' on older lines and
-- vendor orders). The name keeps its readable text for older Pis.
ALTER TABLE line_items
    ADD COLUMN portion text NOT NULL DEFAULT '' CHECK (portion IN ('', 'first', 'second')),
    ADD COLUMN mode    text NOT NULL DEFAULT '' CHECK (mode IN ('', 'add', 'only', 'no', 'specialty'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE line_items DROP COLUMN mode, DROP COLUMN portion;
-- +goose StatementEnd
