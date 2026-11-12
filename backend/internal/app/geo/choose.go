package geo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"quiccpos/main/internal/infra/database/models"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// HandleChoose is the rpc.InboundHandler for geo.choose {order_id, index}:
// staff picked one of the candidates of a check result. The pick is saved
// pinned in geo_cache for the input address, then the delivery is routed and
// pushed again as located / pinned with confirmed_address.
func (s *Service) HandleChoose(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b struct {
		OrderID string `json:"order_id"`
		Index   *int   `json:"index"`
	}
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("%w: geo.choose: %v", rpc.ErrBadPayload, err)
	}
	id, err := uuid.Parse(b.OrderID)
	if err != nil || b.Index == nil || *b.Index < 0 {
		return nil, fmt.Errorf("%w: geo.choose needs {order_id, index >= 0}", rpc.ErrBadPayload)
	}
	idx := *b.Index
	fail := func(code string) (any, error) { return map[string]any{"ok": false, "code": code}, nil }

	o, err := s.q.GetOrderForGeo(ctx, models.GetOrderForGeoParams{OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID)})
	switch {
	case err == nil:
		g, err := s.q.GetOrderGeo(ctx, pgUUID(id))
		if errors.Is(err, pgx.ErrNoRows) {
			return fail("no_candidates")
		}
		if err != nil {
			return nil, err
		}
		cands := decodeCandidates(g.Candidates)
		if g.State != StateCheck || len(cands) == 0 {
			return fail("no_candidates")
		}
		if idx >= len(cands) {
			return fail("bad_index")
		}
		c := cands[idx]
		addr, ok := orderAddress(o.FulfillmentAddress)
		if err := s.q.UpsertGeoCachePinned(ctx, models.UpsertGeoCachePinnedParams{AddressKey: PinCacheKey(scope.TenantID, cacheKey(id, addr, ok)), Lat: c.Lat, Lng: c.Lng}); err != nil {
			return nil, err
		}
		conf, _ := json.Marshal(c.Confirmed())
		if err := s.q.ResetOrderGeoForChoice(ctx, models.ResetOrderGeoForChoiceParams{OrderID: pgUUID(id), ConfirmedAddress: conf}); err != nil {
			return nil, err
		}
	case errors.Is(err, pgx.ErrNoRows):
		return fail("not_found")
	default:
		return nil, err
	}
	s.logger.Info().Ctx(ctx).Str("order_id", id.String()).Int("index", idx).Msg("geo: candidate chosen")
	s.poke()
	return map[string]any{"ok": true}, nil
}
