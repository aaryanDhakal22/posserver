-- +goose Up
-- +goose StatementBegin

-- Driver shifts (clock-in → clock-out) mirrored from the agent's driver.shift
-- floor events — a record for reports (not wages). Keyed by the agent's shift
-- id; the clock-out arrives as a second event for the same shift.
CREATE TABLE driver_shifts (
    id          uuid        PRIMARY KEY,
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    driver_id   uuid        NOT NULL,
    driver_name text        NOT NULL,
    clock_in    timestamptz NOT NULL,
    clock_out   timestamptz,
    auto_out    boolean     NOT NULL DEFAULT false,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_driver_shifts_location ON driver_shifts (location_id, clock_in DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS driver_shifts;
-- +goose StatementEnd
