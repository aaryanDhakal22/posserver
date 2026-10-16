-- +goose Up
-- +goose StatementBegin

-- floor_at: the `at` of the newest agent floor event applied to the order —
-- an older (late / overlapping) event can no longer overwrite the kitchen
-- number, ready-by time or driver with stale values.
ALTER TABLE orders ADD COLUMN floor_at timestamptz;

-- Agent driver / shift ids are only unique within the agent that made them: a
-- Pi re-linked to another location keeps its drivers. Key both mirrors by
-- location so that can't collide (or silently skip) across stores.
ALTER TABLE drivers DROP CONSTRAINT drivers_pkey;
ALTER TABLE drivers ADD PRIMARY KEY (location_id, id);
ALTER TABLE driver_shifts DROP CONSTRAINT driver_shifts_pkey;
ALTER TABLE driver_shifts ADD PRIMARY KEY (location_id, id);

-- Locations that had printers before 00004 have no receipt printer flagged:
-- flag the oldest one (the owner can move it in /app/store).
UPDATE printers p SET is_receipt = true
WHERE p.id IN (
    SELECT DISTINCT ON (location_id) id FROM printers
    WHERE location_id NOT IN (SELECT location_id FROM printers WHERE is_receipt)
    ORDER BY location_id, created_at, id
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE driver_shifts DROP CONSTRAINT driver_shifts_pkey;
ALTER TABLE driver_shifts ADD PRIMARY KEY (id);
ALTER TABLE drivers DROP CONSTRAINT drivers_pkey;
ALTER TABLE drivers ADD PRIMARY KEY (id);
ALTER TABLE orders DROP COLUMN IF EXISTS floor_at;
-- +goose StatementEnd
