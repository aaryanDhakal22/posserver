-- +goose Up
-- +goose StatementBegin
-- Pi updates + printer auto-follow (main 2.0.6, 2026-10-21).
--
-- printers.mac: the printer's hardware address as the Pi learned it ('' =
--   not known yet). Set when the Pi adds a printer it found on the network or
--   follows a printer to a new IP (RPC printer.add / printer.moved).
ALTER TABLE printers ADD COLUMN mac text NOT NULL DEFAULT '';

-- printer_ip_changes: every printer IP change, by the Pi following a printer
--   that moved (source 'agent') or a manager editing it in /app/store
--   (source 'manager'). Goes with the printer when it is deleted.
CREATE TABLE printer_ip_changes (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL,
    location_id uuid        NOT NULL,
    printer_id  uuid        NOT NULL REFERENCES printers(id) ON DELETE CASCADE,
    old_ip      text        NOT NULL DEFAULT '',
    new_ip      text        NOT NULL DEFAULT '',
    mac         text        NOT NULL DEFAULT '',
    source      text        NOT NULL CHECK (source IN ('agent', 'manager')),
    at          timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_printer_ip_changes_location ON printer_ip_changes (location_id, at DESC);

-- agent_updates: each Pi update / roll back, keyed by the request id main or
--   the Pi made. main inserts it (state 'requested') before asking the agent;
--   the agent's agent.update.status reports move it on (running → ok /
--   up_to_date / failed / rolled_back). An update started on the Pi itself
--   (its web page, a fresh card's boot) arrives only as a report and is
--   inserted then. 'rejected' / 'unreachable' are main's own end states.
CREATE TABLE agent_updates (
    id           uuid        PRIMARY KEY,
    tenant_id    uuid        NOT NULL,
    location_id  uuid        NOT NULL,
    action       text        NOT NULL,
    requested_by text        NOT NULL DEFAULT '',
    force        boolean     NOT NULL DEFAULT false,
    state        text        NOT NULL,
    from_version text        NOT NULL DEFAULT '',
    to_version   text        NOT NULL DEFAULT '',
    error        text        NOT NULL DEFAULT '',
    requested_at timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_agent_updates_location ON agent_updates (location_id, requested_at DESC);
CREATE INDEX idx_agent_updates_requested ON agent_updates (requested_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_updates;
DROP TABLE IF EXISTS printer_ip_changes;
ALTER TABLE printers DROP COLUMN IF EXISTS mac;
-- +goose StatementEnd
