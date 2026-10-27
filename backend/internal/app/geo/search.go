package geo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"quiccpos/main/internal/infra/database/models"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/observability"
	"quiccpos/main/internal/rpc"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
)

// Staff address search (2.2.2): RPC geo.search {order_id, query} — free text
// typed on the tablet ("1613 Wadsworth Way"). main answers {ok:true} at once,
// then searches the store's area (StoreArea: every geocoder, merged, at most
// MaxSearchResults) and pushes order.geo for the order: state check with the
// matches (staff pick one with geo.choose, as for any check) or not_found
// with a note. Online orders and external stops alike.

const (
	// MaxSearchQuery is the longest query geo.search takes (characters).
	MaxSearchQuery = 200
	// SearchGap: one search per order per this long.
	SearchGap = 3 * time.Second
	// searchTimeout bounds one search (Nominatim's 1 req/s gate may queue it).
	searchTimeout = 60 * time.Second

	NoteSearchNoMatch = "no match for that search"
	NoteSearchFailed  = "the search failed, try again"
	NoteSearchNoArea  = "the store has no position to search near"
)

// HandleSearch is the rpc.InboundHandler for geo.search {order_id, query}.
// Replies {ok:true}, or {ok:false, code: not_found | too_soon}.
func (s *Service) HandleSearch(ctx context.Context, scope sseBroker.Scope, payload json.RawMessage) (any, error) {
	var b struct {
		OrderID string `json:"order_id"`
		Query   string `json:"query"`
	}
	if err := json.Unmarshal(payload, &b); err != nil {
		return nil, fmt.Errorf("%w: geo.search: %v", rpc.ErrBadPayload, err)
	}
	id, err := uuid.Parse(b.OrderID)
	query := strings.Join(strings.Fields(b.Query), " ")
	if err != nil || query == "" || utf8.RuneCountInString(query) > MaxSearchQuery {
		return nil, fmt.Errorf("%w: geo.search needs {order_id, query (1-%d characters)}", rpc.ErrBadPayload, MaxSearchQuery)
	}
	online := true
	if _, err := s.q.GetOrderForGeo(ctx, models.GetOrderForGeoParams{OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID)}); errors.Is(err, pgx.ErrNoRows) {
		online = false
		if _, err := s.q.GetExternalStop(ctx, models.GetExternalStopParams{OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID)}); errors.Is(err, pgx.ErrNoRows) {
			return map[string]any{"ok": false, "code": "not_found"}, nil
		} else if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	key := id.String()
	if !s.searches.Allow(key) {
		return map[string]any{"ok": false, "code": "too_soon", "retry_after_ms": s.searches.RetryAfter(key).Milliseconds()}, nil
	}
	s.searches.Add(key)
	if online {
		// A pickup order (or one the sweep has not reached) gets its row now.
		if _, err := s.q.InsertOrderGeo(ctx, models.InsertOrderGeoParams{OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID)}); err != nil {
			return nil, err
		}
	}
	s.logger.Info().Ctx(ctx).Str("order_id", id.String()).Bool("online", online).Msg("geo: address search requested")
	bg := context.WithoutCancel(ctx)
	s.spawn(func() {
		ctx, cancel := context.WithTimeout(bg, searchTimeout)
		defer cancel()
		s.runSearch(ctx, scope, id, online, query)
	})
	return map[string]any{"ok": true}, nil
}

// runSearch searches, saves the answer on the order / stop and wakes the
// worker to push it.
func (s *Service) runSearch(ctx context.Context, scope sseBroker.Scope, id uuid.UUID, online bool, query string) {
	cands, note := s.search(ctx, scope, query)
	state := StateCheck
	if len(cands) == 0 {
		state = StateNotFound
	}
	var n int64
	var err error
	if online {
		n, err = s.q.SaveOrderGeoSearch(ctx, models.SaveOrderGeoSearchParams{
			OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID),
			State: state, Candidates: candidatesJSON(cands), Note: note,
		})
	} else {
		err = s.tx(ctx, func(q Queries) error {
			if n, err = q.SaveExternalStopSearch(ctx, models.SaveExternalStopSearchParams{
				OrderID: pgUUID(id), TenantID: pgUUID(scope.TenantID), LocationID: pgUUID(scope.LocationID),
				State: state, Note: note,
			}); err != nil || n == 0 {
				return err // 0: forgotten meanwhile
			}
			return q.UpsertExternalWorkSearch(ctx, models.UpsertExternalWorkSearchParams{
				OrderID: pgUUID(id), Candidates: candidatesJSON(cands), DiscardAt: ts(s.now().Add(AddressDiscardAfter)),
			})
		})
	}
	log := s.logger.With().Str("order_id", id.String()).Bool("online", online).Logger()
	switch {
	case err != nil:
		log.Warn().Ctx(ctx).Err(err).Msg("geo: saving the address search failed")
		return
	case n == 0:
		log.Info().Ctx(ctx).Msg("geo: address search answered for an order that is gone")
		return
	}
	attrs := []attribute.KeyValue{attribute.String("result", state), attribute.String("via", "search")}
	if !online {
		attrs = append(attrs, attribute.String("kind", "external"))
	}
	s.located.Add(ctx, 1, observability.WithStore(ctx, scope.TenantID, attrs...))
	log.Info().Ctx(ctx).Str("state", state).Int("candidates", len(cands)).Str("note", note).Msg("geo: address search answered")
	s.poke()
}

// search looks the query up inside the store's area. No candidates comes
// with the note to show staff.
func (s *Service) search(ctx context.Context, scope sseBroker.Scope, query string) ([]Candidate, string) {
	var bbox []float64
	var store *Point
	state := ""
	if l, err := s.tenancy.GetLocation(ctx, scope.LocationID); err == nil && l != nil {
		bbox = l.MapBBox
		if l.Lat != nil && l.Lng != nil {
			store = &Point{Lat: *l.Lat, Lng: *l.Lng}
		}
		state = StoreAddress(l.AddressLine1, l.AddressLine2).State
	} else if err != nil {
		s.logger.Warn().Ctx(ctx).Err(err).Msg("geo: location lookup for a search failed")
		return nil, NoteSearchFailed
	}
	box, ok := StoreArea(bbox, store)
	if !ok {
		return nil, NoteSearchNoArea
	}
	cands, err := searchIn(ctx, s.Providers().Geocoder, Address{Oneline: withState(query, state)}, box)
	switch {
	case err == nil:
		return cands, ""
	case errors.Is(err, ErrNotFound):
		return nil, NoteSearchNoMatch
	default:
		s.logger.Warn().Ctx(ctx).Err(err).Msg("geo: address search failed (provider)")
		return nil, NoteSearchFailed
	}
}

// withState appends the store's state to a query that doesn't name it ("1613
// Wadsworth Way" → "1613 Wadsworth Way, MD"): the Census needs a state or a
// zip to match anything.
func withState(query, state string) string {
	state = strings.TrimSpace(state)
	if state == "" {
		return query
	}
	if regexp.MustCompile(`(?i)(^|[^a-z])` + regexp.QuoteMeta(state) + `([^a-z]|$)`).MatchString(query) {
		return query
	}
	return query + ", " + state
}
