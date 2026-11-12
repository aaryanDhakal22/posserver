package observability

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

type memExporter struct {
	mu   sync.Mutex
	recs []sdklog.Record
}

func (m *memExporter) Export(_ context.Context, rs []sdklog.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rs {
		m.recs = append(m.recs, r.Clone())
	}
	return nil
}
func (m *memExporter) Shutdown(context.Context) error   { return nil }
func (m *memExporter) ForceFlush(context.Context) error { return nil }

func bridged(t *testing.T) (zerolog.Logger, *memExporter, *bytes.Buffer) {
	t.Helper()
	exp := &memExporter{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))
	t.Cleanup(func() { _ = lp.Shutdown(context.Background()) })
	var out bytes.Buffer
	w := NewOTLPWriter(&out, lp.Logger("test"))
	return zerolog.New(w).With().Timestamp().Logger().Hook(TraceHook{}), exp, &out
}

func attrs(r sdklog.Record) map[string]any {
	m := map[string]any{}
	r.WalkAttributes(func(kv attribute.KeyValue) bool {
		m[string(kv.Key)] = kv.Value.AsInterface()
		return true
	})
	return m
}

func TestBridgeShipsFieldsAsAttributes(t *testing.T) {
	lg, exp, out := bridged(t)
	tid, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	sid, _ := trace.SpanIDFromHex("0102030405060708")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled}))

	lg.Warn().Ctx(ctx).
		Str("order_id", "o-1").
		Int64("amount_cents", -500).
		Float64("ratio", 0.5).
		Bool("flagged", true).
		Str("pin", "1234").
		Str("manager_pin", "9999").
		Str("session_token", "qcs_x").
		Str("client_key", "p-abc").
		Msg("Counter refund given")

	if len(exp.recs) != 1 {
		t.Fatalf("records = %d, want 1", len(exp.recs))
	}
	r := exp.recs[0]
	if r.Body().AsString() != "Counter refund given" || r.SeverityText() != "warn" {
		t.Fatalf("body %q severity %q", r.Body().AsString(), r.SeverityText())
	}
	if r.TraceID() != tid || r.SpanID() != sid {
		t.Fatalf("trace context %s/%s", r.TraceID(), r.SpanID())
	}
	a := attrs(r)
	if a["order_id"] != "o-1" || a["amount_cents"] != int64(-500) || a["ratio"] != 0.5 || a["flagged"] != true || a["client_key"] != "p-abc" {
		t.Fatalf("attributes %v", a)
	}
	for _, k := range []string{"pin", "manager_pin", "session_token", "level", "time", "message", "trace_id", "span_id"} {
		if _, ok := a[k]; ok {
			t.Errorf("attribute %q must not be bridged: %v", k, a)
		}
	}
	// stderr keeps the line as before (pin included: the local log is unchanged).
	if !strings.Contains(out.String(), `"order_id":"o-1"`) || !strings.Contains(out.String(), `"trace_id":"0102030405060708090a0b0c0d0e0f10"`) {
		t.Fatalf("stderr line changed: %s", out.String())
	}
}

func TestBridgeNestedAndNoTrace(t *testing.T) {
	lg, exp, _ := bridged(t)
	lg.Info().Interface("obj", map[string]int{"a": 1}).Strs("list", []string{"x", "y"}).Msg("m")
	a := attrs(exp.recs[0])
	if a["obj"] != `{"a":1}` || a["list"] != `["x","y"]` {
		t.Fatalf("nested %v", a)
	}
	if exp.recs[0].TraceID().IsValid() {
		t.Fatal("no trace context expected")
	}
}

func TestDroppedLogKey(t *testing.T) {
	for _, k := range []string{"pin", "PIN", "manager_pin", "password", "new_password_hash", "admin_secret", "token", "api_key", "authorization", "device_key"} {
		if !DroppedLogKey(k) {
			t.Errorf("%q should be dropped", k)
		}
	}
	for _, k := range []string{"order_id", "amount_cents", "staff_id", "client_key", "spinner", "pinned", "tokens_minted"} {
		if DroppedLogKey(k) {
			t.Errorf("%q should be kept", k)
		}
	}
}
