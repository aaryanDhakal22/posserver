// Package safego runs goroutines that can't take main down: a panic is
// recovered, logged at error with its stack (and the trace id when the
// context carries a span), and counted as main.panics{where}.
//
// Go is for one-shot work; Loop is for the long-lived loops started in
// cmd/api (a loop that panicked is started again after a short pause, so a
// poison input can't silently stop the dispatcher or the capture worker).
package safego

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	logger atomic.Pointer[zerolog.Logger]

	panicsOnce sync.Once
	panics     metric.Int64Counter
)

// SetLogger installs the logger panics are written to (cmd/api, at start).
// Until then they go to a no-op logger (and are still counted).
func SetLogger(l zerolog.Logger) {
	l = l.With().Str("module", "safego").Logger()
	logger.Store(&l)
}

func log() *zerolog.Logger {
	if l := logger.Load(); l != nil {
		return l
	}
	nop := zerolog.Nop()
	return &nop
}

func counter() metric.Int64Counter {
	panicsOnce.Do(func() {
		c, err := otel.Meter("quiccpos/main").Int64Counter("main.panics",
			metric.WithDescription("Panics recovered in main (where = the goroutine or the HTTP layer)"))
		if err == nil {
			panics = c
		}
	})
	return panics
}

// Count records one recovered panic in main.panics{where} (for a caller
// that logs the panic itself, like the HTTP recover middleware).
func Count(ctx context.Context, where string) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c := counter(); c != nil {
		c.Add(ctx, 1, metric.WithAttributes(attribute.String("where", where)))
	}
}

// Recovered logs and counts one recovered panic value. Exported for the HTTP
// recover middleware and per-message recovers, which handle the value
// themselves (return a 500, leave a message for redrive).
func Recovered(ctx context.Context, where string, rec any) {
	if ctx == nil {
		ctx = context.Background()
	}
	Count(ctx, where)
	ev := log().Error().Ctx(ctx).Str("where", where).Str("panic", fmt.Sprint(rec)).Str("stack", string(debug.Stack()))
	ev.Msg("panic recovered")
}

// Guard is for `defer safego.Guard(ctx, "where")`: it recovers, logs and
// counts. The function it guards returns normally.
func Guard(ctx context.Context, where string) {
	if rec := recover(); rec != nil {
		Recovered(ctx, where, rec)
	}
}

// Go runs fn in a goroutine that recovers a panic.
func Go(ctx context.Context, where string, fn func(ctx context.Context)) {
	go func() {
		defer Guard(ctx, where)
		fn(ctx)
	}()
}

// Run calls fn synchronously and reports whether it panicked (recovered,
// logged, counted).
func Run(ctx context.Context, where string, fn func(ctx context.Context)) (panicked bool) {
	defer func() {
		if rec := recover(); rec != nil {
			Recovered(ctx, where, rec)
			panicked = true
		}
	}()
	fn(ctx)
	return false
}

// Restart pauses between a panic and the next start of a Loop: 1 s, doubling
// to 30 s. Tests shorten it.
var restartMin, restartMax = time.Second, 30 * time.Second

// Loop runs fn (a blocking loop that returns when ctx ends) in a goroutine.
// A panic is recovered and fn starts again after a pause, until ctx is done.
// A normal return ends the loop.
func Loop(ctx context.Context, where string, fn func(ctx context.Context)) {
	go RunLoop(ctx, where, fn)
}

// RunLoop is Loop without the goroutine (it blocks).
func RunLoop(ctx context.Context, where string, fn func(ctx context.Context)) {
	wait := restartMin
	for {
		if !Run(ctx, where, fn) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		log().Warn().Str("where", where).Dur("after", wait).Msg("restarting a loop after a panic")
		if wait *= 2; wait > restartMax {
			wait = restartMax
		}
	}
}
