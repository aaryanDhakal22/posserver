package sqsconsumer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/observability"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
)

type fakeSQS struct {
	mu        sync.Mutex
	probeErrs int // GetQueueAttributes fails this many times first
	probes    atomic.Int32
	receives  atomic.Int32
	deletes   atomic.Int32
	batches   [][]sqstypes.Message
}

func (f *fakeSQS) GetQueueAttributes(ctx context.Context, _ *sqs.GetQueueAttributesInput, _ ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	if int(f.probes.Add(1)) <= f.probeErrs {
		return nil, errors.New("dial tcp: connection refused")
	}
	return &sqs.GetQueueAttributesOutput{}, nil
}

func (f *fakeSQS) ReceiveMessage(ctx context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	f.receives.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.batches) == 0 {
		select { // an empty long poll
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
		return &sqs.ReceiveMessageOutput{}, nil
	}
	b := f.batches[0]
	f.batches = f.batches[1:]
	return &sqs.ReceiveMessageOutput{Messages: b}, nil
}

func (f *fakeSQS) DeleteMessage(ctx context.Context, _ *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	f.deletes.Add(1)
	return &sqs.DeleteMessageOutput{}, nil
}

type panicCaster struct{ calls atomic.Int32 }

func (*panicCaster) Source() order.Source { return order.SourceOnline }
func (p *panicCaster) Cast(context.Context, order.CastInput) (*order.Order, error) {
	p.calls.Add(1)
	panic("caster blew up")
}

type noCreate struct{}

func (noCreate) Create(context.Context, *order.Order) error { return errors.New("unreachable") }

func newTestConsumer(t *testing.T, f *fakeSQS, c Caster) *Consumer {
	t.Helper()
	meters, err := observability.NewMeters()
	if err != nil {
		t.Fatal(err)
	}
	return &Consumer{
		client: f, queueURL: "q", casters: map[order.Source]Caster{order.SourceOnline: c},
		orderService: noCreate{}, logger: zerolog.Nop(), tracer: otel.Tracer("t"), meters: meters,
	}
}

func shortBackoff(t *testing.T) {
	initialBackoff, maxBackoff = time.Millisecond, 4*time.Millisecond
	t.Cleanup(func() { initialBackoff, maxBackoff = 2*time.Second, 60*time.Second })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestPanickingCasterKeepsPollingAndLeavesMessage(t *testing.T) {
	shortBackoff(t)
	msg := sqstypes.Message{MessageId: aws.String("m-1"), Body: aws.String(`{"order_id":"x"}`), ReceiptHandle: aws.String("r")}
	f := &fakeSQS{batches: [][]sqstypes.Message{{msg}, {msg}}}
	caster := &panicCaster{}
	c := newTestConsumer(t, f, caster)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Start(ctx); close(done) }()

	// Both deliveries are handled (the second proves polling went on after a panic).
	waitFor(t, "two casts", func() bool { return caster.calls.Load() == 2 })
	waitFor(t, "a poll after the panics", func() bool { return f.receives.Load() >= 3 })
	cancel()
	<-done
	if n := f.deletes.Load(); n != 0 {
		t.Fatalf("a panicked message was deleted %d times; it must stay for redrive", n)
	}
}

func TestUnreachableSQSRetriesInsteadOfFatal(t *testing.T) {
	shortBackoff(t)
	f := &fakeSQS{probeErrs: 3}
	c := newTestConsumer(t, f, &panicCaster{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Start(ctx); close(done) }()

	waitFor(t, "the probe to succeed after failures", func() bool { return f.receives.Load() >= 1 })
	if f.probes.Load() != 4 {
		t.Fatalf("probes = %d, want 4 (3 failures then ok)", f.probes.Load())
	}
	if !observability.SQSUp() {
		t.Fatal("sqs.up should be 1 once the queue answers")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not end on ctx cancel")
	}
}

func TestUnreachableForeverEndsOnCancel(t *testing.T) {
	shortBackoff(t)
	f := &fakeSQS{probeErrs: 1 << 30}
	c := newTestConsumer(t, f, &panicCaster{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Start(ctx); close(done) }()
	waitFor(t, "a few probes", func() bool { return f.probes.Load() >= 3 })
	if observability.SQSUp() {
		t.Fatal("sqs.up should be 0 while the probe fails")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not end on ctx cancel")
	}
}
