package sse

import (
	"context"
	"testing"

	"quiccpos/main/internal/observability"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

func TestDisconnectDeviceAndBuffer(t *testing.T) {
	meters, _ := observability.NewMeters()
	b := New(zerolog.Nop(), meters)
	ctx := context.Background()
	scope := Scope{TenantID: uuid.New(), LocationID: uuid.New()}
	oldDev, newDev := uuid.New(), uuid.New()
	oldCh, unsubOld := b.SubscribeAs(ctx, scope, oldDev)
	newCh, unsubNew := b.SubscribeAs(ctx, scope, newDev)
	defer unsubNew()

	// A burst well past the old 8-frame buffer is queued, not dropped.
	for i := 0; i < 100; i++ {
		if n := b.Publish(ctx, scope, Frame{Event: EventOrder, Data: []byte("x")}); n != 2 {
			t.Fatalf("frame %d delivered to %d subscribers, want 2", i, n)
		}
	}
	if got := len(newCh); got != 100 {
		t.Fatalf("queued %d, want 100", got)
	}

	if n := b.DisconnectDevice(oldDev); n != 1 {
		t.Fatalf("disconnected %d streams, want 1", n)
	}
	for range oldCh { // drain; the channel must be closed
	}
	if b.SubscribersFor(scope) != 1 {
		t.Fatalf("subscribers = %d, want 1", b.SubscribersFor(scope))
	}
	unsubOld() // safe after a disconnect
	if b.DisconnectDevice(uuid.Nil) != 0 {
		t.Fatal("nil device must not disconnect anything")
	}
}
