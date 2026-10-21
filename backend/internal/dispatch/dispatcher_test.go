package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"quiccpos/main/internal/domain/order"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

type fakeRepo struct {
	mu         sync.Mutex
	pending    map[string][]order.DomainEvent
	dispatched map[string]map[uuid.UUID]bool // handler → eventID → true
	failed     map[string]map[uuid.UUID]string
	failedAt   map[uuid.UUID][]time.Time // eventID → nextAttempt per failure
	resets     [][2]uuid.UUID
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		pending:    map[string][]order.DomainEvent{},
		dispatched: map[string]map[uuid.UUID]bool{},
		failed:     map[string]map[uuid.UUID]string{},
		failedAt:   map[uuid.UUID][]time.Time{},
	}
}

// isDispatched / failure read the fake under its lock (the dispatcher writes
// from its own goroutine).
func (r *fakeRepo) isDispatched(handler string, id uuid.UUID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dispatched[handler][id]
}

func (r *fakeRepo) failure(handler string, id uuid.UUID) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msg, ok := r.failed[handler][id]
	return msg, ok
}

func (r *fakeRepo) enqueue(handler string, ev order.DomainEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending[handler] = append(r.pending[handler], ev)
}

func (r *fakeRepo) PendingEventsForHandler(_ context.Context, handlerName string, limit int) ([]PendingEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	all := r.pending[handlerName]
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]PendingEvent, 0, len(all))
	for _, ev := range all {
		out = append(out, PendingEvent{Event: ev, Attempts: len(r.failedAt[ev.ID])})
	}
	return out, nil
}

func (r *fakeRepo) ResetBackoffForScope(_ context.Context, tenantID, locationID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resets = append(r.resets, [2]uuid.UUID{tenantID, locationID})
	return nil
}

func (r *fakeRepo) MarkDispatched(_ context.Context, eventID uuid.UUID, handlerName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dispatched[handlerName] == nil {
		r.dispatched[handlerName] = map[uuid.UUID]bool{}
	}
	r.dispatched[handlerName][eventID] = true
	r.pending[handlerName] = removeEvent(r.pending[handlerName], eventID)
	return nil
}

func (r *fakeRepo) MarkFailed(_ context.Context, eventID uuid.UUID, handlerName string, errMsg string, next time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failedAt[eventID] = append(r.failedAt[eventID], next)
	if r.failed[handlerName] == nil {
		r.failed[handlerName] = map[uuid.UUID]string{}
	}
	r.failed[handlerName][eventID] = errMsg
	// Pending stays pending — the dispatcher will retry on the next tick.
	return nil
}

func removeEvent(list []order.DomainEvent, id uuid.UUID) []order.DomainEvent {
	out := make([]order.DomainEvent, 0, len(list))
	for _, e := range list {
		if e.ID != id {
			out = append(out, e)
		}
	}
	return out
}

// fakeHandler with configurable behavior per call.
type fakeHandler struct {
	name     string
	calls    atomic.Int32
	respond  func(call int32) error
	lastSeen atomic.Pointer[order.DomainEvent]
}

func (h *fakeHandler) Name() string { return h.name }
func (h *fakeHandler) Handle(_ context.Context, ev order.DomainEvent) error {
	n := h.calls.Add(1)
	h.lastSeen.Store(&ev)
	if h.respond == nil {
		return nil
	}
	return h.respond(n)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeEvent(t order.EventType) order.DomainEvent {
	return order.DomainEvent{
		ID:            uuid.Must(uuid.NewV7()),
		AggregateID:   order.ID(uuid.Must(uuid.NewV7())),
		Type:          t,
		Payload:       []byte(`{"k":"v"}`),
		OccurredAt:    time.Now(),
		SchemaVersion: 1,
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestDispatcher_HappyPath(t *testing.T) {
	repo := newFakeRepo()
	h := &fakeHandler{name: "test"}
	repo.enqueue(h.name, makeEvent(order.EventOrderReceived))

	d := New(repo, []Handler{h}, zerolog.Nop(), WithPollInterval(20*time.Millisecond), WithBatchSize(10))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go d.Run(ctx)

	waitFor(t, 200*time.Millisecond, func() bool { return h.calls.Load() >= 1 })

	if got := h.calls.Load(); got != 1 {
		t.Errorf("Handle called %d times, want exactly 1", got)
	}
	if !repo.isDispatched(h.name, h.lastSeen.Load().ID) {
		t.Error("event was not marked dispatched")
	}
}

func TestDispatcher_TransientErrorRetries(t *testing.T) {
	repo := newFakeRepo()
	// First call fails transiently, subsequent calls succeed.
	h := &fakeHandler{
		name: "flaky",
		respond: func(n int32) error {
			if n == 1 {
				return fmt.Errorf("not ready: %w", ErrTransient)
			}
			return nil
		},
	}
	ev := makeEvent(order.EventOrderReceived)
	repo.enqueue(h.name, ev)

	d := New(repo, []Handler{h}, zerolog.Nop(), WithPollInterval(15*time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	go d.Run(ctx)

	waitFor(t, 300*time.Millisecond, func() bool { return repo.isDispatched(h.name, ev.ID) })

	if got := h.calls.Load(); got < 2 {
		t.Errorf("Handle called %d times; expected at least 2 (one transient failure, then success)", got)
	}
	if msg, ok := repo.failure(h.name, ev.ID); !ok || msg == "" {
		t.Errorf("transient failure not recorded; got %q ok=%v", msg, ok)
	}
	if !repo.isDispatched(h.name, ev.ID) {
		t.Error("event was never marked dispatched after retry")
	}
}

func TestDispatcher_PermanentErrorIsRecorded(t *testing.T) {
	repo := newFakeRepo()
	h := &fakeHandler{
		name: "broken",
		respond: func(n int32) error {
			return errors.New("kaboom")
		},
	}
	ev := makeEvent(order.EventOrderReceived)
	repo.enqueue(h.name, ev)

	d := New(repo, []Handler{h}, zerolog.Nop(), WithPollInterval(15*time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go d.Run(ctx)

	waitFor(t, 100*time.Millisecond, func() bool {
		_, ok := repo.failure(h.name, ev.ID)
		return ok
	})

	if repo.isDispatched(h.name, ev.ID) {
		t.Error("permanent error was incorrectly marked dispatched")
	}
	if msg, _ := repo.failure(h.name, ev.ID); msg == "" {
		t.Error("permanent failure not recorded")
	}
}

func TestDispatcher_HooksFire(t *testing.T) {
	repo := newFakeRepo()
	hSucc := &fakeHandler{name: "ok"}
	hFail := &fakeHandler{name: "bad", respond: func(n int32) error { return errors.New("nope") }}
	repo.enqueue(hSucc.name, makeEvent(order.EventOrderReceived))
	repo.enqueue(hFail.name, makeEvent(order.EventOrderReceived))

	var dispatched, failed atomic.Int32
	hooks := Hooks{
		OnEventDispatched: func(handler string, _ order.EventType) { dispatched.Add(1) },
		OnEventFailed:     func(handler string, _ order.EventType, _ bool) { failed.Add(1) },
	}
	d := New(repo, []Handler{hSucc, hFail}, zerolog.Nop(),
		WithPollInterval(15*time.Millisecond), WithHooks(hooks))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go d.Run(ctx)

	waitFor(t, 100*time.Millisecond, func() bool {
		return dispatched.Load() >= 1 && failed.Load() >= 1
	})

	if dispatched.Load() < 1 {
		t.Errorf("OnEventDispatched fired %d times, want ≥1", dispatched.Load())
	}
	if failed.Load() < 1 {
		t.Errorf("OnEventFailed fired %d times, want ≥1", failed.Load())
	}
}

func TestDispatcher_HandlerNamesPreserveOrder(t *testing.T) {
	a := &fakeHandler{name: "alpha"}
	b := &fakeHandler{name: "beta"}
	c := &fakeHandler{name: "gamma"}
	d := New(newFakeRepo(), []Handler{a, b, c}, zerolog.Nop())
	got := d.HandlerNames()
	want := []string{"alpha", "beta", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("HandlerNames len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("HandlerNames[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDispatcher_WakeFiresImmediately(t *testing.T) {
	repo := newFakeRepo()
	h := &fakeHandler{name: "test"}
	// Long poll interval so a normal tick won't happen during the test.
	d := New(repo, []Handler{h}, zerolog.Nop(), WithPollInterval(10*time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	go d.Run(ctx)

	// Give Run a moment to enter its first iteration (which processes once
	// unconditionally), then enqueue and Wake.
	time.Sleep(20 * time.Millisecond)
	repo.enqueue(h.name, makeEvent(order.EventOrderReceived))
	d.Wake()

	waitFor(t, 200*time.Millisecond, func() bool { return h.calls.Load() >= 1 })
	if h.calls.Load() == 0 {
		t.Error("Wake did not trigger immediate dispatch")
	}
}

// waitFor polls a condition until true or deadline. Avoids time.Sleep races.
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// Backoff
// ---------------------------------------------------------------------------

func TestBackoffDelay_GrowsCapsAndJitters(t *testing.T) {
	lo := func(n int64) int64 { return 0 }     // jitter floor
	hi := func(n int64) int64 { return n - 1 } // jitter ceiling

	cases := []struct {
		transient bool
		attempts  int
		wantMin   time.Duration
		wantMax   time.Duration
	}{
		{true, 0, 500 * time.Millisecond, 1 * time.Second},
		{true, 1, 1 * time.Second, 2 * time.Second},
		{true, 3, 4 * time.Second, 8 * time.Second},
		{true, 5, 15 * time.Second, 30 * time.Second},  // 32s → capped at 30s
		{true, 60, 15 * time.Second, 30 * time.Second}, // overflow-safe
		{false, 0, 2500 * time.Millisecond, 5 * time.Second},
		{false, 4, 40 * time.Second, 80 * time.Second},
		{false, 20, 5 * time.Minute, 10 * time.Minute}, // capped at 10m
	}
	for _, tc := range cases {
		gotLo := backoffDelay(tc.attempts, tc.transient, lo)
		gotHi := backoffDelay(tc.attempts, tc.transient, hi)
		if gotLo != tc.wantMin || gotHi != tc.wantMax {
			t.Errorf("transient=%v attempts=%d: range [%v, %v], want [%v, %v]",
				tc.transient, tc.attempts, gotLo, gotHi, tc.wantMin, tc.wantMax)
		}
	}
}

func TestDispatcher_FailureSchedulesBackoffFromAttempts(t *testing.T) {
	repo := newFakeRepo()
	ev := makeEvent(order.EventOrderReceived)
	repo.enqueue("h", ev)
	repo.failedAt[ev.ID] = make([]time.Time, 4) // pretend 4 prior attempts
	h := &fakeHandler{name: "h", respond: func(int32) error { return fmt.Errorf("down: %w", ErrTransient) }}
	d := New(repo, []Handler{h}, zerolog.Nop())

	before := time.Now()
	d.tick(context.Background())

	got := repo.failedAt[ev.ID]
	if len(got) != 5 {
		t.Fatalf("expected one new failure recorded, have %d", len(got))
	}
	delay := got[4].Sub(before)
	if delay < 8*time.Second || delay > 17*time.Second { // attempt 4 → [8s,16s] (+slack)
		t.Fatalf("5th attempt delay = %v, want within [8s, 16s]", delay)
	}
}

func TestDispatcher_WakeScopeResetsBackoffAndWakes(t *testing.T) {
	repo := newFakeRepo()
	d := New(repo, nil, zerolog.Nop())
	tid, lid := uuid.New(), uuid.New()
	d.WakeScope(context.Background(), tid, lid)
	if len(repo.resets) != 1 || repo.resets[0] != [2]uuid.UUID{tid, lid} {
		t.Fatalf("resets = %v, want one for %v/%v", repo.resets, tid, lid)
	}
	select {
	case <-d.wake:
	default:
		t.Fatal("WakeScope did not wake the dispatcher")
	}
}
