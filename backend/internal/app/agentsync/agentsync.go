// Package agentsync is main's side of the agent's reliability + recovery
// contract:
//
//   - Delivery guarantee: the agent acks every order it stored (RPC report
//     `order.ack`); orders still unacked are re-sent over SSE every few
//     seconds while the agent is connected, until acked. A frame lost to a
//     dropped stream, a full buffer or a crash is therefore never lost.
//   - State mirror: the agent reports its whole floor state (`agent.state`:
//     open batch, kitchen counter, drivers + queue + open shifts, settings) on
//     every change, and its health (`agent.health`).
//   - Recovery: a blank agent (a swapped SD card) links with the store manager
//     login (Link) — main mints its key, revokes every other agent of that
//     location, and hands over the runtime secrets — then pulls the open batch
//     back (Restore).
//   - Backups: the agent's nightly SQLite copy, newest 2 per location.
//   - Offline pager: during the location's opening hours, an agent that has
//     been gone for 2 minutes pages the owner + boss.
package agentsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	tenancyApp "quiccpos/main/internal/app/tenancy"
	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/models"
	"quiccpos/main/internal/infra/pushover"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/observability"
	"quiccpos/main/internal/rpc"
	"quiccpos/main/internal/shared/ratelimit"
	sseTransport "quiccpos/main/internal/transport/sse"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// RPC report types the agent sends.
const (
	MsgOrderAck    = "order.ack"
	MsgAgentState  = "agent.state"
	MsgAgentHealth = "agent.health"
)

// Secrets are the runtime secrets main hands a freshly linked agent (from
// main's own env, AGENT_SECRET_*). None of them is ever written to a card.
type Secrets struct {
	PushoverAppToken string `json:"pushover_app_token,omitempty"`
	PushoverUserKey  string `json:"pushover_user_key,omitempty"`
	PushoverBossKey  string `json:"pushover_boss_key,omitempty"`
	OTLPEndpoint     string `json:"otlp_endpoint,omitempty"`
	OTLPHeaders      string `json:"otlp_headers,omitempty"`
	TailscaleAuthKey string `json:"tailscale_authkey,omitempty"`
}

// Broker is the slice of the SSE broker the service uses.
type Broker interface {
	Publish(ctx context.Context, scope sseBroker.Scope, frame sseBroker.Frame) int
	SubscribersFor(scope sseBroker.Scope) int
}

// OrderLoader loads an order aggregate in scope (the order repository).
type OrderLoader interface {
	GetByID(ctx context.Context, scope order.Scope, id order.ID) (*order.Order, error)
}

// DriverMirror is the floor read model's driver roster (fallback for restore
// when the agent never reported its state).
type DriverMirror interface {
	ListDrivers(ctx context.Context, scope order.Scope) ([]order.Driver, error)
}

type Service struct {
	pool    *pgxpool.Pool
	orders  OrderLoader
	drivers DriverMirror
	broker  Broker
	tenancy *tenancyApp.Service
	secrets Secrets
	pager   *pushover.Client
	logger  zerolog.Logger

	redelivered metric.Int64Counter
	acked       metric.Int64Counter
	ackLatency  metric.Float64Histogram
	links       metric.Int64Counter
	pages       metric.Int64Counter

	// RedeliverAfter is how long an order may stay unacked before it is
	// re-sent (and between re-sends). OfflineAfter is how long an agent may
	// be gone during opening hours before it pages.
	RedeliverAfter time.Duration
	OfflineAfter   time.Duration

	offMu     sync.Mutex
	offSince  map[uuid.UUID]time.Time // location → first seen offline
	offPaged  map[uuid.UUID]bool
	linkLimit *ratelimit.Window // failed link attempts
	// printersChanged pushes a fresh store_config after the agent changed
	// the printers (OnPrintersChanged); nil = no push.
	printersChanged func(ctx context.Context, tenantID, locationID uuid.UUID)
	// linkMu serializes links: each mints a key then revokes every other
	// agent, so two overlapping links (a double tap, a retry after a
	// timeout) would revoke each other's new key. main is one process.
	linkMu sync.Mutex
}

func New(pool *pgxpool.Pool, orders OrderLoader, drivers DriverMirror, broker Broker, tenancy *tenancyApp.Service,
	secrets Secrets, pager *pushover.Client, logger zerolog.Logger) *Service {
	m := otel.Meter(observability.MeterName)
	s := &Service{
		pool: pool, orders: orders, drivers: drivers, broker: broker, tenancy: tenancy,
		secrets: secrets, pager: pager,
		logger:         logger.With().Str("module", "agentsync").Logger(),
		RedeliverAfter: 10 * time.Second,
		OfflineAfter:   2 * time.Minute,
		offSince:       map[uuid.UUID]time.Time{},
		offPaged:       map[uuid.UUID]bool{},
		linkLimit:      ratelimit.New(5, 10*time.Minute),
	}
	s.redelivered, _ = m.Int64Counter("orders.redelivered", metric.WithDescription("Orders re-sent to an agent that had not acked them"))
	s.acked, _ = m.Int64Counter("orders.agent_acked", metric.WithDescription("Orders acknowledged by an agent"))
	s.ackLatency, _ = observability.NewHopHistogram(m, "orders.ack.latency", "Order persisted → acked by the agent (order.ack)")
	s.links, _ = m.Int64Counter("agents.linked", metric.WithDescription("Agents linked with a manager login (Pi set up / replaced)"))
	s.pages, _ = m.Int64Counter("agents.offline_pages", metric.WithDescription("Agent-offline pages sent during opening hours"))
	unacked, _ := m.Int64ObservableGauge("orders.unacked", metric.WithDescription("Orders an agent has not acked for over a minute (should be 0)"))
	_, _ = m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		// One series per store (0 when none), so a store's series never goes stale.
		rows, err := models.New(pool).CountUnackedOrdersByTenant(ctx, models.CountUnackedOrdersByTenantParams{
			OlderThan: ts(time.Now().Add(-time.Minute)), NewerThan: ts(time.Now().Add(-12 * time.Hour)),
		})
		if err != nil {
			return nil
		}
		for _, r := range rows {
			o.ObserveInt64(unacked, r.Unacked, observability.WithStore(ctx, fromPg(r.TenantID)))
		}
		return nil
	}, unacked)
	return s
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func pgUUID(u uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: u, Valid: true} }

func fromPg(u pgtype.UUID) uuid.UUID {
	if !u.Valid {
		return uuid.Nil
	}
	return uuid.UUID(u.Bytes)
}

// --- delivery guarantee ------------------------------------------------------

// HandleAck is the rpc.InboundHandler for order.ack: {"order_ids": [...]}.
func (s *Service) HandleAck(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var body struct {
		OrderIDs []string `json:"order_ids"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, fmt.Errorf("%w: order.ack: %v", rpc.ErrBadPayload, err)
	}
	ids := make([]pgtype.UUID, 0, len(body.OrderIDs))
	for _, raw := range body.OrderIDs {
		if id, err := uuid.Parse(raw); err == nil {
			ids = append(ids, pgUUID(id))
		}
	}
	if len(ids) == 0 {
		return map[string]any{"acked": 0}, nil
	}
	received, err := models.New(s.pool).MarkOrdersAcked(ctx, models.MarkOrdersAckedParams{
		TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID), Ids: ids,
	})
	if err != nil {
		return nil, err
	}
	n := int64(len(received))
	store := observability.WithStore(ctx, scope.TenantID)
	s.acked.Add(ctx, n, store)
	// Only first acks (agent_ack_at was NULL), so a repeated ack never
	// re-measures. received_at = when main persisted the order.
	now := time.Now()
	for _, r := range received {
		if r.Valid {
			observability.RecordLatency(ctx, s.ackLatency, r.Time, now, store)
		}
	}
	return map[string]any{"acked": n}, nil
}

// RunRedeliver re-sends unacked orders until ctx ends (checks every second;
// an order is re-sent once it has been unacked for RedeliverAfter, then again
// every RedeliverAfter). Only scopes with a connected agent are sent to.
func (s *Service) RunRedeliver(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	lastSent := map[uuid.UUID]time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		rows, err := models.New(s.pool).ListUnackedOrders(ctx, models.ListUnackedOrdersParams{
			OlderThan: ts(now.Add(-s.RedeliverAfter)), NewerThan: ts(now.Add(-12 * time.Hour)), MaxRows: 200,
		})
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn().Err(err).Msg("list unacked orders failed")
			}
			continue
		}
		seen := map[uuid.UUID]bool{}
		for _, r := range rows {
			id := fromPg(r.ID)
			seen[id] = true
			if at, ok := lastSent[id]; ok && now.Sub(at) < s.RedeliverAfter {
				continue
			}
			scope := sseBroker.Scope{TenantID: fromPg(r.TenantID), LocationID: fromPg(r.LocationID)}
			if s.broker.SubscribersFor(scope) == 0 {
				continue // nobody to send to; the outbox / catch-up deliver on connect
			}
			if s.resend(ctx, scope, id) {
				lastSent[id] = now
			}
		}
		for id := range lastSent { // forget orders that got acked
			if !seen[id] {
				delete(lastSent, id)
			}
		}
	}
}

func (s *Service) resend(ctx context.Context, scope sseBroker.Scope, id uuid.UUID) bool {
	o, err := s.orders.GetByID(ctx, order.Scope{TenantID: order.TenantID(scope.TenantID), LocationID: order.LocationID(scope.LocationID)}, order.ID(id))
	if err != nil {
		s.logger.Warn().Err(err).Str("order_id", id.String()).Msg("redeliver: load order failed")
		return false
	}
	data, err := json.Marshal(sseTransport.EventFromSnapshot(ctx, o.Snapshot(), true))
	if err != nil {
		return false
	}
	n := s.broker.Publish(ctx, scope, sseBroker.Frame{Event: sseBroker.EventOrder, Data: data})
	if n > 0 {
		s.redelivered.Add(ctx, 1, observability.WithStore(ctx, scope.TenantID))
		s.logger.Info().Ctx(ctx).Str("order_id", id.String()).Msg("order re-sent to agent (not acked yet)")
	}
	return n > 0
}

// --- state + health reports ------------------------------------------------

type stateReport struct {
	At    time.Time       `json:"at"`
	State json.RawMessage `json:"state"`
}

// HandleState stores the agent's floor state (newest wins).
func (s *Service) HandleState(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var r stateReport
	if err := json.Unmarshal(payload, &r); err != nil || r.At.IsZero() || len(r.State) == 0 {
		return nil, fmt.Errorf("%w: agent.state needs {at, state}", rpc.ErrBadPayload)
	}
	if err := models.New(s.pool).UpsertAgentState(ctx, models.UpsertAgentStateParams{
		LocationID: pgUUID(scope.LocationID), TenantID: pgUUID(scope.TenantID), State: r.State, At: ts(r.At),
	}); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// HandleHealth stores the agent's latest health report.
func (s *Service) HandleHealth(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var r struct {
		At       time.Time `json:"at"`
		DeviceID string    `json:"device_id"`
	}
	if err := json.Unmarshal(payload, &r); err != nil {
		return nil, fmt.Errorf("%w: agent.health: %v", rpc.ErrBadPayload, err)
	}
	if r.At.IsZero() {
		r.At = time.Now()
	}
	dev := pgtype.UUID{}
	if id, err := uuid.Parse(r.DeviceID); err == nil {
		dev = pgUUID(id)
	}
	if err := models.New(s.pool).UpsertAgentHealth(ctx, models.UpsertAgentHealthParams{
		LocationID: pgUUID(scope.LocationID), TenantID: pgUUID(scope.TenantID), DeviceID: dev, Health: payload, At: ts(r.At),
	}); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// AgentHealth is one location's latest agent health report (fleet view).
type AgentHealth struct {
	LocationID   uuid.UUID       `json:"location_id"`
	TenantID     uuid.UUID       `json:"tenant_id"`
	LocationName string          `json:"location_name"`
	Connected    bool            `json:"connected"`
	At           time.Time       `json:"at"`
	Health       json.RawMessage `json:"health"`
}

// Fleet lists every location's latest health + whether its agent is connected.
func (s *Service) Fleet(ctx context.Context) ([]AgentHealth, error) {
	rows, err := models.New(s.pool).ListAgentHealth(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AgentHealth, 0, len(rows))
	for _, r := range rows {
		sc := sseBroker.Scope{TenantID: fromPg(r.TenantID), LocationID: fromPg(r.LocationID)}
		out = append(out, AgentHealth{LocationID: sc.LocationID, TenantID: sc.TenantID, LocationName: r.LocationName,
			Connected: s.broker.SubscribersFor(sc) > 0, At: r.At.Time, Health: r.Health})
	}
	return out, nil
}

// errNotLinked etc. are mapped to HTTP statuses by the handler.
var (
	ErrBadCredentials = errors.New("wrong username or password")
	ErrThrottled      = errors.New("too many attempts — wait a few minutes")
)
