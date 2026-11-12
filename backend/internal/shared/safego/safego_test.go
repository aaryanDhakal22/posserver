package safego

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/trace"
)

func capture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	l := zerolog.New(&buf).Hook(zerolog.HookFunc(func(e *zerolog.Event, _ zerolog.Level, _ string) {
		if ctx := e.GetCtx(); ctx != nil {
			if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
				e.Str("trace_id", sc.TraceID().String())
			}
		}
	}))
	SetLogger(l)
	t.Cleanup(func() { SetLogger(zerolog.Nop()) })
	return &buf
}

func spanCtx() context.Context {
	tid, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	sid, _ := trace.SpanIDFromHex("0102030405060708")
	return trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid}))
}

func TestRunRecoversWithStackAndTrace(t *testing.T) {
	buf := capture(t)
	if !Run(spanCtx(), "test.run", func(context.Context) { panic("boom") }) {
		t.Fatal("Run did not report the panic")
	}
	out := buf.String()
	for _, want := range []string{`"panic":"boom"`, `"where":"test.run"`, `"stack":"goroutine`, `"trace_id":"0102030405060708090a0b0c0d0e0f10"`, `"level":"error"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s: %s", want, out)
		}
	}
	if Run(context.Background(), "test.ok", func(context.Context) {}) {
		t.Fatal("a clean run reported a panic")
	}
}

func TestGoSurvivesPanic(t *testing.T) {
	capture(t)
	done := make(chan struct{})
	Go(context.Background(), "test.go", func(context.Context) {
		defer close(done)
		panic("x")
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine never ran")
	}
}

func TestLoopRestartsAfterPanicAndStopsOnReturn(t *testing.T) {
	capture(t)
	restartMin, restartMax = time.Millisecond, 2*time.Millisecond
	t.Cleanup(func() { restartMin, restartMax = time.Second, 30*time.Second })
	var runs atomic.Int32
	RunLoop(context.Background(), "test.loop", func(context.Context) {
		if runs.Add(1) < 3 {
			panic("again")
		}
	})
	if got := runs.Load(); got != 3 {
		t.Fatalf("runs = %d, want 3 (two panics, then a normal return)", got)
	}
}

func TestLoopEndsWithContext(t *testing.T) {
	capture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var runs atomic.Int32
	RunLoop(ctx, "test.loop", func(context.Context) { runs.Add(1); panic("x") })
	if runs.Load() != 1 {
		t.Fatalf("runs = %d, want 1 (ctx already done)", runs.Load())
	}
}
