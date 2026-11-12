-- +goose Up
-- +goose StatementBegin
-- Tablet diagnostics (main 2.0.1, 2026-10-20).
--
-- tablet_report_tokens: a Quibase tablet's write-only credential for sending a
--   diagnostics report straight to main when the Pi is down
--   (X-Quibase-Report-Key). Minted through the tablet's agent; one active token
--   per (location, tablet_id) — minting again revokes the previous one — and at
--   most 10 active per location. Not tied to the agent device, so linking a new
--   Pi keeps them. Only the SHA-256 hash is stored.
CREATE TABLE tablet_report_tokens (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    uuid        NOT NULL,
    location_id  uuid        NOT NULL,
    tablet_id    text        NOT NULL,
    tablet_name  text        NOT NULL DEFAULT '',
    token_hash   text        NOT NULL UNIQUE,
    key_prefix   text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at   timestamptz,
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_tablet_report_tokens_active ON tablet_report_tokens (location_id) WHERE revoked_at IS NULL;

-- diag_reports: one troubleshooting report (report v1) — sent via the Pi
--   (RPC diag.report) or directly by the tablet. The id is client-generated, so
--   a retried send dedupes on the primary key. Pruned after 180 days.
CREATE TABLE diag_reports (
    id            uuid        PRIMARY KEY,
    tenant_id     uuid        NOT NULL,
    location_id   uuid        NOT NULL,
    tablet_id     text        NOT NULL DEFAULT '',
    tablet_name   text        NOT NULL DEFAULT '',
    via           text        NOT NULL CHECK (via IN ('pi', 'direct')),
    kind          text        NOT NULL,
    scenario      text        NOT NULL DEFAULT '',
    outcome       text        NOT NULL,
    headline      text        NOT NULL DEFAULT '',
    cause         text        NOT NULL DEFAULT '',
    fixed_by      text        NOT NULL DEFAULT '',
    app_version   text        NOT NULL DEFAULT '',
    agent_version text        NOT NULL DEFAULT '',
    report        jsonb       NOT NULL,
    created_at    timestamptz NOT NULL,
    received_at   timestamptz NOT NULL DEFAULT now(),
    paged_at      timestamptz,
    resolved_at   timestamptz,
    FOREIGN KEY (location_id, tenant_id) REFERENCES locations(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_diag_reports_location ON diag_reports (location_id, received_at DESC);
CREATE INDEX idx_diag_reports_received ON diag_reports (received_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS diag_reports;
DROP TABLE IF EXISTS tablet_report_tokens;
-- +goose StatementEnd
