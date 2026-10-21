package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/observability"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "quiccpos/main/rpc"

var (
	// ErrAgentOffline means no agent is currently subscribed to the scope's SSE
	// stream, so a command can't be delivered.
	ErrAgentOffline = errors.New("agent offline (no SSE subscribers for scope)")
	// ErrTimeout means the command was sent but no reply arrived in time.
	ErrTimeout = errors.New("rpc request timed out")
)

// CommandPublisher is the slice of *sse.Broker the requester needs: send a
// command frame to a scope + know whether an agent is connected.
type CommandPublisher interface {
	PublishCommand(ctx context.Context, scope sseBroker.Scope, data []byte) int
	SubscribersFor(scope sseBroker.Scope) int
}

// InboundHandler processes an agent-initiated message (a report, or a request
// that wants a reply). A non-nil replyPayload is returned to the agent with the
// same correlation id; nil = fire-and-forget.
type InboundHandler func(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (replyPayload any, err error)

type waiter struct {
	scope sseBroker.Scope
	ch    chan Envelope
}

// Requester is the reusable RPC core (a singleton wired in cmd/api/main.go).
type Requester struct {
	pub    CommandPublisher
	logger zerolog.Logger
	meters observability.Meters
	tracer trace.Tracer

	mu      sync.Mutex
	waiters map[string]waiter         // correlation id → pending Request
	inbound map[string]InboundHandler // message type → agent-initiated handler
}

func NewRequester(pub CommandPublisher, meters observability.Meters, lg zerolog.Logger) *Requester {
	return &Requester{
		pub:     pub,
		logger:  lg.With().Str("module", "rpc-requester").Logger(),
		meters:  meters,
		tracer:  otel.Tracer(tracerName),
		waiters: make(map[string]waiter),
		inbound: make(map[string]InboundHandler),
	}
}

// Request sends msgType to the agent(s) for scope and waits for the first
// reply (matched by correlation id) or a timeout. ErrAgentOffline if no agent
// is connected to that scope.
func (r *Requester) Request(ctx context.Context, scope sseBroker.Scope, msgType string, payload any, timeout time.Duration) (Envelope, error) {
	typeAttr := metric.WithAttributes(attribute.String("type", msgType))

	ctx, span := r.tracer.Start(ctx, "rpc.request",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("rpc.type", msgType)),
	)
	defer span.End()

	r.meters.RPCRequests.Add(ctx, 1, typeAttr)
	start := time.Now()

	if r.pub.SubscribersFor(scope) == 0 {
		r.meters.RPCOffline.Add(ctx, 1, typeAttr)
		span.SetStatus(codes.Error, "agent offline")
		return Envelope{}, ErrAgentOffline
	}
	raw, err := marshalPayload(payload)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "marshal payload")
		return Envelope{}, err
	}
	id := uuid.NewString()
	data, err := json.Marshal(Envelope{Type: msgType, ID: id, Payload: raw})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "marshal envelope")
		return Envelope{}, err
	}

	ch := make(chan Envelope, 1)
	r.mu.Lock()
	r.waiters[id] = waiter{scope: scope, ch: ch}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.waiters, id)
		r.mu.Unlock()
	}()

	if r.pub.PublishCommand(ctx, scope, data) == 0 {
		r.meters.RPCOffline.Add(ctx, 1, typeAttr)
		span.SetStatus(codes.Error, "agent offline")
		return Envelope{}, ErrAgentOffline
	}

	select {
	case reply := <-ch:
		r.meters.RPCRoundtrip.Record(ctx, msSince(start), typeAttr)
		r.meters.RPCReplies.Add(ctx, 1, metric.WithAttributes(
			attribute.String("type", msgType),
			attribute.String("result", "ok"),
		))
		return reply, nil
	case <-time.After(timeout):
		r.meters.RPCRoundtrip.Record(ctx, msSince(start), typeAttr)
		r.meters.RPCTimeouts.Add(ctx, 1, typeAttr)
		span.SetStatus(codes.Error, "rpc timeout")
		return Envelope{}, ErrTimeout
	case <-ctx.Done():
		span.RecordError(ctx.Err())
		span.SetStatus(codes.Error, "context cancelled")
		return Envelope{}, ctx.Err()
	}
}

// msSince returns the elapsed milliseconds since t as a float (sub-ms precision).
func msSince(t time.Time) float64 {
	return float64(time.Since(t).Microseconds()) / 1000.0
}

// Deliver routes an inbound reply to its waiting Request. Returns true if it
// matched a pending correlation id. The scope is asserted (defense-in-depth) so
// a cross-tenant device can't inject a reply for another scope's id.
func (r *Requester) Deliver(scope sseBroker.Scope, env Envelope) bool {
	r.mu.Lock()
	w, ok := r.waiters[env.ID]
	r.mu.Unlock()
	if !ok || w.scope != scope {
		return false
	}
	select {
	case w.ch <- env: // buffered(1); first reply wins, later dupes dropped
	default:
	}
	return true
}

// RegisterInbound registers a handler for an agent-INITIATED message type.
func (r *Requester) RegisterInbound(msgType string, h InboundHandler) {
	r.mu.Lock()
	r.inbound[msgType] = h
	r.mu.Unlock()
}

// DispatchInbound runs the registered handler for an agent-initiated message.
// (replyEnvelope, handled, err): handled=false means unknown type (no-op);
// a zero replyEnvelope means the handler was fire-and-forget.
func (r *Requester) DispatchInbound(ctx context.Context, scope sseBroker.Scope, env Envelope) (Envelope, bool, error) {
	ctx, span := r.tracer.Start(ctx, "rpc.inbound",
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attribute.String("rpc.type", env.Type)),
	)
	defer span.End()

	r.mu.Lock()
	h, ok := r.inbound[env.Type]
	r.mu.Unlock()
	if !ok {
		r.meters.RPCInbound.Add(ctx, 1, metric.WithAttributes(
			attribute.String("type", env.Type),
			attribute.String("result", "unknown"),
		))
		return Envelope{}, false, nil
	}
	reply, err := h(ctx, scope, env.Payload)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "inbound handler failed")
		r.meters.RPCInbound.Add(ctx, 1, metric.WithAttributes(
			attribute.String("type", env.Type),
			attribute.String("result", "error"),
		))
		return Envelope{}, true, err
	}
	r.meters.RPCInbound.Add(ctx, 1, metric.WithAttributes(
		attribute.String("type", env.Type),
		attribute.String("result", "ok"),
	))
	if reply == nil {
		return Envelope{}, true, nil
	}
	raw, err := marshalPayload(reply)
	if err != nil {
		span.RecordError(err)
		return Envelope{}, true, err
	}
	return Envelope{Type: env.Type + ".reply", ID: env.ID, Payload: raw}, true, nil
}

func marshalPayload(p any) (json.RawMessage, error) {
	if p == nil {
		return nil, nil
	}
	if raw, ok := p.(json.RawMessage); ok {
		return raw, nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return b, nil
}
