package staffApp

import (
	"context"
	"encoding/json"
	"time"

	"quiccpos/main/internal/domain/menu"
	"quiccpos/main/internal/domain/menuview"
	sseBroker "quiccpos/main/internal/infra/sse"

	"github.com/google/uuid"
)

// MsgMenuChanged is the command main pushes when a counter store's menu
// version moves: {version}.
const MsgMenuChanged = "menu.changed"

// MenuWatchEvery is how often the watcher looks at the connected stores.
const MenuWatchEvery = 3 * time.Second

// MenuVersions reads a store's menu version and views (MenuBankRepository).
type MenuVersions interface {
	Version(ctx context.Context, locationID uuid.UUID) (menu.Version, error)
	ListViews(ctx context.Context, locationID uuid.UUID) ([]menuview.View, error)
}

// Connected lists the scopes with a connected agent (*sse.Broker).
type Connected interface {
	Scopes() []sseBroker.Scope
}

// WatchMenus pushes `menu.changed {version}` to a connected store's Pi when
// its menu version moves, and once when it first sees the store connected
// (main may have restarted since the Pi's last pull) — only for stores whose
// counter view has a layout (a store without a counter terminal never gets
// one). Runs until ctx ends.
func (s *Service) WatchMenus(ctx context.Context, menus MenuVersions, conn Connected) {
	seen := map[sseBroker.Scope]int64{}
	t := time.NewTicker(MenuWatchEvery)
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
			v, err := menus.Version(ctx, sc.LocationID)
			if err != nil {
				continue
			}
			last, ok := seen[sc]
			if ok && last == v.Version {
				continue
			}
			seen[sc] = v.Version
			if s.hasCounterLayout(ctx, menus, sc.LocationID) {
				s.pushMenu(ctx, sc, v.Version)
			}
		}
		for sc := range seen {
			if !now[sc] {
				delete(seen, sc)
			}
		}
	}
}

func (s *Service) hasCounterLayout(ctx context.Context, menus MenuVersions, locationID uuid.UUID) bool {
	views, err := menus.ListViews(ctx, locationID)
	if err != nil {
		return false
	}
	for _, v := range views {
		if v.Kind == menuview.KindCounter && v.HasLayout {
			return true
		}
	}
	return false
}

func (s *Service) pushMenu(ctx context.Context, sc sseBroker.Scope, version int64) {
	if s.push == nil {
		return
	}
	payload, _ := json.Marshal(map[string]int64{"version": version})
	data, _ := json.Marshal(map[string]any{"type": MsgMenuChanged, "id": uuid.NewString(), "payload": json.RawMessage(payload)})
	n := s.push.PublishCommand(ctx, sc, data)
	s.logger.Debug().Ctx(ctx).Str("location_id", sc.LocationID.String()).Int64("version", version).Int("delivered", n).Msg("menu.changed pushed")
}
