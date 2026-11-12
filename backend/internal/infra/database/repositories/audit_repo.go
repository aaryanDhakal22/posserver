package repositories

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"quiccpos/main/internal/infra/database/auditconn"
)

// AuditRepository writes main's own audit rows (transport middleware AuditWrites) and
// reads the audit log (baseline 2026-11-12: the table is append-only, the
// database triggers write the row changes). Plain SQL.
type AuditRepository struct {
	pool *pgxpool.Pool
}

func NewAuditRepository(pool *pgxpool.Pool) *AuditRepository { return &AuditRepository{pool: pool} }

// WriteApp stores one admin / manager write.
func (r *AuditRepository) WriteApp(ctx context.Context, e auditconn.Entry) error {
	detail, _ := json.Marshal(map[string]any{"status": e.Status, "params": e.Params, "impersonated_by": e.Actor.ImpersonatedBy})
	_, err := r.pool.Exec(ctx, `INSERT INTO audit_log (at, tenant_id, location_id, actor_kind, actor_id, actor_name, source,
		action, entity, entity_id, detail, trace_id, request_id, ip)
		VALUES ($1, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, $4, $5, $6, 'app', $7, $8, $9, $10, $11, $12, $13)`,
		e.At, e.Actor.TenantID, e.Actor.LocationID, e.Actor.Kind, e.Actor.ID, e.Actor.Name,
		e.Method, e.Route, e.EntityID, detail, e.TraceID, e.Actor.RequestID, e.Actor.IP)
	return err
}

// AuditRow is one audit_log row as the read API answers it.
type AuditRow struct {
	ID         int64           `json:"id"`
	At         time.Time       `json:"at"`
	TenantID   *uuid.UUID      `json:"tenant_id"`
	LocationID *uuid.UUID      `json:"location_id"`
	ActorKind  string          `json:"actor_kind"`
	ActorID    string          `json:"actor_id"`
	ActorName  string          `json:"actor_name"`
	Source     string          `json:"source"`
	Action     string          `json:"action"`
	Entity     string          `json:"entity"`
	EntityID   string          `json:"entity_id"`
	Before     json.RawMessage `json:"before"`
	After      json.RawMessage `json:"after"`
	Detail     json.RawMessage `json:"detail"`
	TraceID    string          `json:"trace_id"`
	RequestID  string          `json:"request_id"`
	IP         string          `json:"ip"`
}

// AuditFilter narrows List. LocationID nil = every store (admin only);
// BeforeID 0 = the newest; Entity / EntityID "" = any.
type AuditFilter struct {
	LocationID *uuid.UUID
	BeforeID   int64
	Entity     string
	EntityID   string
	Limit      int
}

// List pages the log newest first (by id: the order rows were written).
func (r *AuditRepository) List(ctx context.Context, f AuditFilter) ([]AuditRow, error) {
	rows, err := r.pool.Query(ctx, `SELECT id, at, tenant_id, location_id, actor_kind, actor_id, actor_name, source, action,
		entity, entity_id, before, after, detail, trace_id, request_id, ip
		FROM audit_log
		WHERE ($1::uuid IS NULL OR location_id = $1) AND ($2::bigint = 0 OR id < $2)
		  AND ($3 = '' OR entity = $3) AND ($4 = '' OR entity_id = $4)
		ORDER BY id DESC LIMIT $5`, f.LocationID, f.BeforeID, f.Entity, f.EntityID, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditRow{}
	for rows.Next() {
		var a AuditRow
		var before, after, detail []byte
		if err := rows.Scan(&a.ID, &a.At, &a.TenantID, &a.LocationID, &a.ActorKind, &a.ActorID, &a.ActorName, &a.Source,
			&a.Action, &a.Entity, &a.EntityID, &before, &after, &detail, &a.TraceID, &a.RequestID, &a.IP); err != nil {
			return nil, err
		}
		a.Before, a.After, a.Detail = raw(before), raw(after), raw(detail)
		out = append(out, a)
	}
	return out, rows.Err()
}

func raw(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}
