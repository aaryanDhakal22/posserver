package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/observability"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type fakePub struct {
	subs      int
	published chan Envelope
}

func (f *fakePub) SubscribersFor(_ sseBroker.Scope) int { return f.subs }
func (f *fakePub) PublishCommand(_ context.Context, _ sseBroker.Scope, data []byte) int {
	var env Envelope
	_ = json.Unmarshal(data, &env)
	f.published <- env
	return f.subs
}

func newReq(subs int) (*Requester, *fakePub) {
	pub := &fakePub{subs: subs, published: make(chan Envelope, 1)}
	meters, _ := observability.NewMeters()
	return NewRequester(pub, meters, zerolog.Nop()), pub
}

func scope() sseBroker.Scope {
	return sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}
}

func TestRequestReplyRoundTrip(t *testing.T) {
	r, pub := newReq(1)
	sc := scope()

	type result struct {
		env Envelope
		err error
	}
	done := make(chan result, 1)
	go func() {
		env, err := r.Request(context.Background(), sc, MsgPing, map[string]any{"x": 1}, 2*time.Second)
		done <- result{env, err}
	}()

	sent := <-pub.published // captures the generated correlation id
	if sent.Type != MsgPing || sent.ID == "" {
		t.Fatalf("unexpected command: %+v", sent)
	}
	// Simulate the agent reply.
	if !r.Deliver(sc, Envelope{Type: MsgPing + ".reply", ID: sent.ID, Payload: json.RawMessage(`{"pong":true}`)}) {
		t.Fatal("Deliver did not match the pending request")
	}

	res := <-done
	if res.err != nil {
		t.Fatalf("Request errored: %v", res.err)
	}
	if string(res.env.Payload) != `{"pong":true}` {
		t.Fatalf("reply payload: %s", res.env.Payload)
	}
}

func TestRequestOffline(t *testing.T) {
	r, _ := newReq(0)
	_, err := r.Request(context.Background(), scope(), MsgPing, nil, time.Second)
	if !errors.Is(err, ErrAgentOffline) {
		t.Fatalf("want ErrAgentOffline, got %v", err)
	}
}

func TestRequestTimeout(t *testing.T) {
	r, pub := newReq(1)
	go func() { <-pub.published }() // consume the command, never reply
	_, err := r.Request(context.Background(), scope(), MsgPing, nil, 50*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
}

func TestDeliverScopeGuard(t *testing.T) {
	r, pub := newReq(1)
	sc := scope()
	go func() { _, _ = r.Request(context.Background(), sc, MsgPing, nil, time.Second) }()
	sent := <-pub.published
	// A reply from a DIFFERENT scope must not match the waiter.
	if r.Deliver(scope(), Envelope{ID: sent.ID}) {
		t.Fatal("Deliver matched a reply from the wrong scope")
	}
}

func TestDispatchInbound(t *testing.T) {
	r, _ := newReq(1)
	r.RegisterInbound("printer.report", func(_ context.Context, _ sseBroker.Scope, payload json.RawMessage) (any, error) {
		return map[string]any{"ack": true}, nil
	})
	reply, handled, err := r.DispatchInbound(context.Background(), scope(),
		Envelope{Type: "printer.report", ID: uuid.NewString(), Payload: json.RawMessage(`{}`)})
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if string(reply.Payload) != `{"ack":true}` {
		t.Fatalf("reply: %s", reply.Payload)
	}

	// Unknown type → not handled, no reply.
	_, handled, _ = r.DispatchInbound(context.Background(), scope(), Envelope{Type: "nope"})
	if handled {
		t.Fatal("unknown type should not be handled")
	}
}

// FailType fails the pending requests of one type for one scope at once
// (the agent's stream is gone), leaving other types and scopes waiting; a
// reply that arrives afterwards finds no waiter and is counted as late, not
// dispatched as an inbound message.
func TestFailTypeAndLateReply(t *testing.T) {
	r, pub := newReq(1)
	pub.published = make(chan Envelope, 4)
	sc, other := scope(), scope()
	errs := make(chan error, 3)
	ask := func(s sseBroker.Scope, typ string) {
		go func() {
			_, err := r.Request(context.Background(), s, typ, nil, 3*time.Second)
			errs <- err
		}()
	}
	ask(sc, "terminal.relay")
	relay := <-pub.published
	ask(sc, MsgPing)
	ping := <-pub.published
	ask(other, "terminal.relay")
	otherRelay := <-pub.published

	if n := r.FailType(sc, "terminal.relay"); n != 1 {
		t.Fatalf("failed %d requests, want 1", n)
	}
	select {
	case err := <-errs:
		if !errors.Is(err, ErrStreamLost) {
			t.Fatalf("the relayed call: %v, want ErrStreamLost", err)
		}
	case <-time.After(time.Second):
		t.Fatal("the relayed call was not failed at once")
	}
	if n := r.FailType(sc, "terminal.relay"); n != 0 {
		t.Fatalf("failing again: %d", n)
	}
	// The ping of the same scope and the other scope's relay still get their answers.
	if !r.Deliver(sc, Envelope{Type: MsgPing + ".reply", ID: ping.ID}) || !r.Deliver(other, Envelope{Type: "terminal.relay.reply", ID: otherRelay.ID}) {
		t.Fatal("other requests lost their waiters")
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("other request: %v", err)
		}
	}
	// The failed call's reply arrives late: no waiter, not an inbound type, no error.
	late := Envelope{Type: "terminal.relay.reply", ID: relay.ID, Payload: json.RawMessage(`{"status":201}`)}
	wait := time.Now().Add(time.Second)
	for r.Deliver(sc, late) && time.Now().Before(wait) { // the waiter is removed when Request returns
		time.Sleep(5 * time.Millisecond)
	}
	if r.Deliver(sc, late) {
		t.Fatal("a late reply still found a waiter")
	}
	reply, handled, err := r.DispatchInbound(context.Background(), sc, late)
	if handled || err != nil || reply.ID != "" {
		t.Fatalf("a late reply: handled %v err %v reply %+v", handled, err, reply)
	}
}
