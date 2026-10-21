package agentsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"quiccpos/main/internal/infra/database/models"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// Pi updates (2.0.6). An update / roll back starts from the admin website or
// the console (RequestUpdate → RPC agent.update / agent.rollback) or on the Pi
// itself (its web page, a fresh card's boot). Either way the agent reports
// every state change as agent.update.status, which HandleUpdateStatus upserts
// into agent_updates by the request id.

// Update actions.
const (
	UpdateActionUpdate   = "update"
	UpdateActionRollback = "rollback"
)

// agent_updates states main sets itself: the row before the agent answered,
// and the two ways asking can end without the agent taking it on.
const (
	UpdateStateRequested   = "requested"
	UpdateStateRejected    = "rejected"    // the agent said no (error = its code)
	UpdateStateUnreachable = "unreachable" // no agent connected, or no reply
)

// UpdateTimeout is how long main waits for the agent's immediate reply (the
// install itself runs afterwards on the Pi).
const UpdateTimeout = 5 * time.Second

// ErrUnknownLocation: the location doesn't exist or isn't the tenant's.
var ErrUnknownLocation = errors.New("unknown tenant/location")

// Commander sends a command to a scope's agent and waits for its reply (the
// rpc.Requester).
type Commander interface {
	Request(ctx context.Context, scope sseBroker.Scope, msgType string, payload any, timeout time.Duration) (rpc.Envelope, error)
}

// UpdateRequest asks a location's Pi to update (or roll back).
type UpdateRequest struct {
	TenantID    uuid.UUID
	LocationID  uuid.UUID
	Action      string // UpdateActionUpdate | UpdateActionRollback
	Force       bool   // update only: skip the after-close rule (still waits for idle)
	RequestedBy string // "admin" | "console"
}

// UpdateResult is the admin response: the agent's reply to agent.update /
// agent.rollback. State is the agent's ("requested" | "waiting_for_idle"), or
// "rejected" (Code says why) / "unreachable".
type UpdateResult struct {
	ID       string `json:"id"`
	Accepted bool   `json:"accepted"`
	State    string `json:"state"`
	Code     string `json:"code"`
}

// RequestUpdate records the request (state "requested") and then asks the
// agent. The result is stored on the row: the agent's state when accepted,
// "rejected" with its code, or "unreachable" (rpc.ErrAgentOffline /
// rpc.ErrTimeout are returned with the result so the handler can answer
// 503 / 504). An agent older than 2.0.6 never replies → unreachable.
func (s *Service) RequestUpdate(ctx context.Context, cmd Commander, req UpdateRequest) (UpdateResult, error) {
	if req.Action != UpdateActionUpdate && req.Action != UpdateActionRollback {
		return UpdateResult{}, fmt.Errorf("unknown update action %q", req.Action)
	}
	loc, err := s.tenancy.GetLocation(ctx, req.LocationID)
	if err != nil || loc.TenantID != req.TenantID {
		return UpdateResult{}, ErrUnknownLocation
	}
	id := uuid.New()
	q := models.New(s.pool)
	if err := q.InsertAgentUpdate(ctx, models.InsertAgentUpdateParams{
		ID: pgUUID(id), TenantID: pgUUID(req.TenantID), LocationID: pgUUID(req.LocationID),
		Action: req.Action, RequestedBy: req.RequestedBy, Force: req.Force && req.Action == UpdateActionUpdate,
	}); err != nil {
		return UpdateResult{}, fmt.Errorf("record update request: %w", err)
	}
	res := UpdateResult{ID: id.String()}
	lg := s.logger.With().Str("update_id", res.ID).Str("action", req.Action).
		Str("location_id", req.LocationID.String()).Str("requested_by", req.RequestedBy).Logger()

	msgType, payload := rpc.MsgAgentUpdate, any(map[string]any{"id": res.ID, "force": req.Force, "requested_by": req.RequestedBy})
	if req.Action == UpdateActionRollback {
		msgType, payload = rpc.MsgAgentRollback, map[string]any{"id": res.ID, "requested_by": req.RequestedBy}
	}
	scope := sseBroker.Scope{TenantID: req.TenantID, LocationID: req.LocationID}
	// The row must be settled even if the caller hangs up mid-request.
	settleCtx := context.WithoutCancel(ctx)

	env, err := cmd.Request(ctx, scope, msgType, payload, UpdateTimeout)
	if err != nil {
		why := "no reply from the Pi (an agent older than 2.0.6 can't update remotely)"
		if errors.Is(err, rpc.ErrAgentOffline) {
			why = "the Pi is not connected"
		}
		res.State = UpdateStateUnreachable
		s.settleUpdate(settleCtx, id, UpdateStateUnreachable, why)
		lg.Warn().Ctx(ctx).Err(err).Msg("pi update request not delivered")
		return res, err
	}
	var reply struct {
		Accepted bool   `json:"accepted"`
		State    string `json:"state"`
		Code     string `json:"code"`
	}
	if err := json.Unmarshal(env.Payload, &reply); err != nil {
		res.State, res.Code = UpdateStateRejected, "bad_reply"
		s.settleUpdate(settleCtx, id, UpdateStateRejected, "bad_reply")
		return res, nil
	}
	res.Accepted, res.Code = reply.Accepted, reply.Code
	if !reply.Accepted {
		res.State = UpdateStateRejected
		s.settleUpdate(settleCtx, id, UpdateStateRejected, reply.Code)
		lg.Info().Ctx(ctx).Str("code", reply.Code).Msg("pi update request rejected by the agent")
		return res, nil
	}
	res.State = reply.State
	if res.State == "" {
		res.State = UpdateStateRequested
	}
	if res.State != UpdateStateRequested {
		s.settleUpdate(settleCtx, id, res.State, "")
	}
	lg.Info().Ctx(ctx).Str("state", res.State).Msg("pi update request accepted")
	return res, nil
}

// settleUpdate stores main's own outcome while the row is still "requested"
// (a status report the agent already sent is newer and wins).
func (s *Service) settleUpdate(ctx context.Context, id uuid.UUID, state, errText string) {
	if _, err := models.New(s.pool).SettleAgentUpdateRequest(ctx, models.SettleAgentUpdateRequestParams{
		ID: pgUUID(id), State: state, Error: errText,
	}); err != nil {
		s.logger.Warn().Ctx(ctx).Err(err).Str("update_id", id.String()).Msg("store update outcome failed")
	}
}

// updateIDNamespace derives a stable uuid for a request id that isn't one
// (the contract allows [A-Za-z0-9-]{1,64}).
var updateIDNamespace = uuid.MustParse("6f1c2f5e-3b1a-4c55-9a43-2d0f6a0c2e06")

var (
	updateIDPattern    = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	updateStatePattern = regexp.MustCompile(`^[a-z_]{1,32}$`)
)

// updateStatus mirrors the agent's update.status object (+ requested_by, force).
type updateStatus struct {
	ID          string     `json:"id"`
	Action      string     `json:"action"`
	RequestedBy string     `json:"requested_by"`
	Force       bool       `json:"force"`
	State       string     `json:"state"`
	FromVersion string     `json:"from_version"`
	ToVersion   string     `json:"to_version"`
	FromSHA     string     `json:"from_sha"`
	ToSHA       string     `json:"to_sha"`
	Error       string     `json:"error"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

// HandleUpdateStatus is the rpc.InboundHandler for agent.update.status: upsert
// the reporting location's agent_updates row by id (inserted when unknown —
// an update started on the Pi itself).
func (s *Service) HandleUpdateStatus(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var r updateStatus
	if err := json.Unmarshal(payload, &r); err != nil {
		return nil, fmt.Errorf("%w: agent.update.status: %v", rpc.ErrBadPayload, err)
	}
	if !updateIDPattern.MatchString(r.ID) || !updateStatePattern.MatchString(r.State) {
		return nil, fmt.Errorf("%w: agent.update.status needs an id and a state", rpc.ErrBadPayload)
	}
	if r.Action != UpdateActionRollback {
		r.Action = UpdateActionUpdate
	}
	id, err := uuid.Parse(r.ID)
	if err != nil {
		id = uuid.NewSHA1(updateIDNamespace, []byte(scope.LocationID.String()+"/"+r.ID))
	}
	requestedAt := time.Now()
	if r.StartedAt != nil && !r.StartedAt.IsZero() {
		requestedAt = *r.StartedAt
	}
	if err := models.New(s.pool).UpsertAgentUpdateStatus(ctx, models.UpsertAgentUpdateStatusParams{
		ID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID),
		Action: r.Action, RequestedBy: clip(r.RequestedBy, 32), Force: r.Force, State: r.State,
		FromVersion: clip(r.FromVersion, 64), ToVersion: clip(r.ToVersion, 64), Error: clip(r.Error, 2000),
		RequestedAt: ts(requestedAt),
	}); err != nil {
		return nil, err
	}
	ev := s.logger.Info()
	if r.State == "failed" || r.State == "rolled_back" {
		ev = s.logger.Warn()
	}
	ev.Ctx(ctx).Str("update_id", id.String()).Str("location_id", scope.LocationID.String()).
		Str("action", r.Action).Str("state", r.State).Str("from_version", r.FromVersion).
		Str("to_version", r.ToVersion).Str("error", r.Error).Msg("pi update status")
	return map[string]any{"ok": true}, nil
}

func clip(v string, n int) string {
	v = strings.TrimSpace(v)
	if len(v) > n {
		return v[:n]
	}
	return v
}

// AgentUpdate is one agent_updates row (admin update history).
type AgentUpdate struct {
	ID          uuid.UUID `json:"id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	LocationID  uuid.UUID `json:"location_id"`
	Action      string    `json:"action"`
	RequestedBy string    `json:"requested_by"`
	Force       bool      `json:"force"`
	State       string    `json:"state"`
	FromVersion string    `json:"from_version"`
	ToVersion   string    `json:"to_version"`
	Error       string    `json:"error"`
	RequestedAt time.Time `json:"requested_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ListUpdates returns update history, newest first — one location's, or every
// location's when locationID is uuid.Nil (limit clamped to 1..200, default 20).
func (s *Service) ListUpdates(ctx context.Context, locationID uuid.UUID, limit int) ([]AgentUpdate, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	loc := pgtype.UUID{}
	if locationID != uuid.Nil {
		loc = pgUUID(locationID)
	}
	rows, err := models.New(s.pool).ListAgentUpdates(ctx, models.ListAgentUpdatesParams{LocationID: loc, MaxRows: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]AgentUpdate, 0, len(rows))
	for _, r := range rows {
		out = append(out, AgentUpdate{
			ID: fromPg(r.ID), TenantID: fromPg(r.TenantID), LocationID: fromPg(r.LocationID),
			Action: r.Action, RequestedBy: r.RequestedBy, Force: r.Force, State: r.State,
			FromVersion: r.FromVersion, ToVersion: r.ToVersion, Error: r.Error,
			RequestedAt: r.RequestedAt.Time, UpdatedAt: r.UpdatedAt.Time,
		})
	}
	return out, nil
}
