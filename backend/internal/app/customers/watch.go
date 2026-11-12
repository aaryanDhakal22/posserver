package customers

import (
	"context"
	"encoding/json"
	"time"

	sseBroker "quiccpos/main/internal/infra/sse"

	"github.com/google/uuid"
)

// MsgCustomersChanged is the command main pushes to a store's Pi when one of
// the store's customers changed (any writer: the website, an import, the
// counter): {at}. The Pi pulls the change feed then (it also polls). Older
// agents ignore it (no handler, no reply).
const MsgCustomersChanged = "customers.changed"

// WatchEvery is how often the watcher looks at the connected stores.
const WatchEvery = 3 * time.Second

// Connected lists the scopes with a connected agent (*sse.Broker).
type Connected interface {
	Scopes() []sseBroker.Scope
}

// Pusher sends a command frame to a store's connected agents (*sse.Broker).
type Pusher interface {
	PublishCommand(ctx context.Context, scope sseBroker.Scope, data []byte) int
}

// LastChanger reads when a store's customers last changed (a write or a
// delete; zero = never) — repositories.CustomerRepository.
type LastChanger interface {
	LastChange(ctx context.Context, locationID uuid.UUID) (time.Time, error)
}

// WatchChanges pushes `customers.changed {at}` to a connected store's Pi when
// its customers' last change moves. The first look at a store only records
// it (the Pi pulls the feed when it connects). Runs until ctx ends.
func (s *Service) WatchChanges(ctx context.Context, last LastChanger, conn Connected, push Pusher) {
	seen := map[sseBroker.Scope]time.Time{}
	t := time.NewTicker(WatchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := map[sseBroker.Scope]bool{}
		for _, sc := range conn.Scopes() {
			now[sc] = true
			at, err := last.LastChange(ctx, sc.LocationID)
			if err != nil {
				continue
			}
			prev, ok := seen[sc]
			seen[sc] = at
			if !ok || !at.After(prev) {
				continue
			}
			payload, _ := json.Marshal(map[string]any{"at": at.UTC()})
			data, _ := json.Marshal(map[string]any{"type": MsgCustomersChanged, "id": uuid.NewString(), "payload": json.RawMessage(payload)})
			n := push.PublishCommand(ctx, sc, data)
			s.logger.Debug().Ctx(ctx).Str("location_id", sc.LocationID.String()).Int("delivered", n).Msg("customers.changed pushed")
		}
		for sc := range seen {
			if !now[sc] {
				delete(seen, sc)
			}
		}
	}
}
