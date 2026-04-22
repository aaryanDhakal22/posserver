package sse

import (
	"encoding/json"
	"sync"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/transport/dto"
)

// Broker fans out published byte slices to all subscribed SSE clients.
type Broker struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

func New() *Broker {
	return &Broker{clients: make(map[chan []byte]struct{})}
}

// Subscribe returns a buffered channel that receives published payloads and a
// function to call when the client disconnects.
func (b *Broker) Subscribe() (chan []byte, func()) {
	ch := make(chan []byte, 8)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.clients, ch)
		close(ch)
		b.mu.Unlock()
	}
}

// PublishOrder satisfies orderSvc.OrderPublisher. It converts the domain order
// to a JSON-encoded DTO and fans it out to all connected SSE clients.
func (b *Broker) PublishOrder(o order.Order) {
	data, err := json.Marshal(dto.FromDomain(o))
	if err != nil {
		return
	}
	b.publish(data)
}

// publish sends raw bytes to every subscribed client. Slow clients are skipped
// (non-blocking send) so a stalled agent never blocks the SQS consumer.
func (b *Broker) publish(data []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.clients {
		select {
		case ch <- data:
		default:
		}
	}
}
