package observability

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// Runtime-health instruments (C2, HARDENING-PLAN R-1 / F-2 / F-3). Kept
// out of Meters so they need no plumbing: the SQS consumer and the recover
// paths record them through these helpers.
//
//	sqs.up                 observable gauge, 1 while the last SQS call worked, else 0
//	sqs.messages.panicked  counter, a message whose handling panicked (left for redrive)
//	main.panics            counter {where}, in internal/shared/safego
//	go.* runtime metrics   go.goroutine.count, go.memory.used, go.memory.gc.goal, … (otel contrib runtime)

var (
	sqsUp       atomic.Int64
	sqsUpOnce   sync.Once
	sqsPanicked metric.Int64Counter
	panicOnce   sync.Once
)

// SetSQSUp records whether main can reach SQS (the sqs.up gauge).
func SetSQSUp(up bool) {
	sqsUpOnce.Do(func() {
		m := otel.Meter(MeterName)
		_, _ = m.Int64ObservableGauge("sqs.up",
			metric.WithDescription("1 while main's last SQS call worked, 0 while it can't reach the queue (it keeps retrying)"),
			metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
				o.Observe(sqsUp.Load())
				return nil
			}))
	})
	if up {
		sqsUp.Store(1)
	} else {
		sqsUp.Store(0)
	}
}

// SQSUp is the current sqs.up value (tests).
func SQSUp() bool { return sqsUp.Load() == 1 }

// SQSMessagePanicked counts one SQS message whose handling panicked.
func SQSMessagePanicked(ctx context.Context) {
	panicOnce.Do(func() {
		c, err := otel.Meter(MeterName).Int64Counter("sqs.messages.panicked",
			metric.WithDescription("SQS messages whose handling panicked (recovered, left on the queue for redrive / the DLQ)"))
		if err == nil {
			sqsPanicked = c
		}
	})
	if sqsPanicked != nil {
		sqsPanicked.Add(ctx, 1)
	}
}

// ErrPprofNotLoopback refuses a PPROF_ADDR that isn't a loopback address.
var ErrPprofNotLoopback = errors.New("PPROF_ADDR must be a loopback address (127.0.0.1:<port> or [::1]:<port>)")

// StartPprof serves net/http/pprof on addr (PPROF_ADDR) on its own
// http.Server, never on main's API port. Empty addr = off (returns nil, nil).
// Only a loopback host is accepted: profiles are reached over SSH. The server
// stops when ctx ends. Returns the bound address.
func StartPprof(ctx context.Context, addr string, lg zerolog.Logger) (net.Addr, error) {
	if addr == "" {
		return nil, nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("PPROF_ADDR %q: %w", addr, err)
	}
	if host == "localhost" {
		host = "127.0.0.1"
		_, port, _ := net.SplitHostPort(addr)
		addr = net.JoinHostPort(host, port)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, ErrPprofNotLoopback
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	lg.Info().Str("addr", ln.Addr().String()).Msg("pprof listening (loopback only)")
	return ln.Addr(), nil
}
